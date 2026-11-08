package chunkbuffer

import (
	"errors"
	"io"
	"math/bits"
	"unsafe"
)

// Slab Arena Constants:
// 64 blocks of 32 KB each = 2,097,152 bytes (2 MB).
// Perfectly tracked with a single 64-bit word (uint64).
const (
	ArenaBlockShift = 15                               // 32 KB per block (2^15)
	ArenaBlockSize  = 1 << ArenaBlockShift             // 32,768 bytes
	ArenaBlockCount = 64                               // 64 blocks = 1 uint64 bitmask
	ArenaTotalBytes = ArenaBlockCount * ArenaBlockSize // 2 MB (2,097,152 bytes)

	// MaxBlocksPerArenaBuffer limits how many 32KB blocks a single buffer can hold (16 * 32KB = 512KB).
	MaxBlocksPerArenaBuffer = 16
)

// ErrArenaExhausted is returned when the arena has no free blocks.
var ErrArenaExhausted = errors.New("arena: out of memory blocks")

// SlabArena is a worker-local, lock-free, zero-allocation memory slab arena.
// It manages a contiguous 2 MB memory slab using a single 64-bit bitmask word.
// All allocations and frees execute in O(1) time (1-3 CPU instructions using TZCNT).
// Because the entire memory region is a single pointerless byte slice, it is
// completely invisible to the Go GC mark phase (noscan).
//
// SlabArena is intended for single-threaded worker loops or goroutines bound
// to a dedicated worker thread (e.g., epoll workers or event loops).
type SlabArena struct {
	mask uint64 // bit i: 1 = allocated, 0 = free
	mem  []byte // contiguous 2 MB backing slab
}

// NewSlabArena preallocates a 2 MB contiguous slab arena.
func NewSlabArena() *SlabArena {
	return &SlabArena{
		mem: make([]byte, ArenaTotalBytes),
	}
}

// AllocBlock acquires a 32 KB block from the arena.
// It executes in ~1-2 CPU clock cycles using bits.TrailingZeros64 (hardware TZCNT/BSF).
// Returns the slice view, the block index, and true if successful.
// If the arena is exhausted, it returns nil, -1, false without allocating.
func (a *SlabArena) AllocBlock() ([]byte, int, bool) {
	if a.mask == ^uint64(0) {
		return nil, -1, false
	}
	idx := bits.TrailingZeros64(^a.mask)
	a.mask |= (uint64(1) << idx)
	offset := idx << ArenaBlockShift
	return a.mem[offset : offset+ArenaBlockSize], idx, true
}

// FreeBlock releases the block identified by idx back to the arena in 1 CPU cycle.
func (a *SlabArena) FreeBlock(idx int) {
	if idx >= 0 && idx < ArenaBlockCount {
		a.mask &= ^(uint64(1) << idx)
	}
}

// Reset instantly releases all allocated blocks back to the arena in 1 CPU cycle.
func (a *SlabArena) Reset() {
	a.mask = 0
}

// InUse returns the number of currently allocated blocks.
func (a *SlabArena) InUse() int {
	return bits.OnesCount64(a.mask)
}

// Available returns the number of free blocks remaining in the arena.
func (a *SlabArena) Available() int {
	return ArenaBlockCount - a.InUse()
}

// ArenaBuffer is a hybrid multi-block buffer backed by a SlabArena.
// It holds up to 16 blocks (512 KB) allocated from the local SlabArena in O(1) time.
// If the payload exceeds 512 KB or the arena is exhausted, it seamlessly
// spills over into a pooled chunkbuffer.Buffer.
type ArenaBuffer struct {
	arena      *SlabArena
	blocks     [MaxBlocksPerArenaBuffer][]byte
	blockIDs   [MaxBlocksPerArenaBuffer]int8
	blockLens  [MaxBlocksPerArenaBuffer]int
	blockCount int
	totalLen   int

	// Fallback to chunkbuffer for payloads > 512 KB or when arena is exhausted
	chunk *Buffer
}

// NewArenaBuffer creates an ArenaBuffer bound to the given SlabArena.
func NewArenaBuffer(arena *SlabArena) *ArenaBuffer {
	b := &ArenaBuffer{arena: arena}
	for i := range b.blockIDs {
		b.blockIDs[i] = -1
	}
	return b
}

// Len returns the total number of buffered bytes.
func (b *ArenaBuffer) Len() int {
	return b.totalLen
}

// HasSpillover returns true if the buffer has spilled over into a fallback chunkbuffer.Buffer.
func (b *ArenaBuffer) HasSpillover() bool {
	return b.chunk != nil
}

// Spillover returns the underlying fallback Buffer if spillover occurred, or nil otherwise.
func (b *ArenaBuffer) Spillover() *Buffer {
	return b.chunk
}

// Write appends p to the buffer.
func (b *ArenaBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}

	remaining := p
	for len(remaining) > 0 {
		// If we have an active tail block with space
		if b.blockCount > 0 && b.chunk == nil {
			tailIdx := b.blockCount - 1
			used := b.blockLens[tailIdx]
			room := ArenaBlockSize - used
			if room > 0 {
				take := len(remaining)
				if take > room {
					take = room
				}
				copy(b.blocks[tailIdx][used:], remaining[:take])
				b.blockLens[tailIdx] += take
				b.totalLen += take
				remaining = remaining[take:]
				continue
			}
		}

		// Need a new block
		if b.blockCount < MaxBlocksPerArenaBuffer && b.arena != nil && b.chunk == nil {
			blk, id, ok := b.arena.AllocBlock()
			if ok {
				idx := b.blockCount
				b.blocks[idx] = blk
				b.blockIDs[idx] = int8(id)
				b.blockLens[idx] = 0
				b.blockCount++

				take := len(remaining)
				if take > ArenaBlockSize {
					take = ArenaBlockSize
				}
				copy(blk, remaining[:take])
				b.blockLens[idx] = take
				b.totalLen += take
				remaining = remaining[take:]
				continue
			}
		}

		// Arena exhausted or reached MaxBlocksPerArenaBuffer: spillover to chunkbuffer
		if b.chunk == nil {
			b.chunk = AcquireBuffer()
		}
		m, err := b.chunk.Write(remaining)
		b.totalLen += m
		if err != nil {
			return n - len(remaining) + m, err
		}
		break
	}

	return n, nil
}

// WriteString appends string s without extra allocations.
func (b *ArenaBuffer) WriteString(s string) (int, error) {
	if len(s) == 0 {
		return 0, nil
	}
	return b.Write(unsafe.Slice(unsafe.StringData(s), len(s)))
}

// WriteByte appends a single byte.
func (b *ArenaBuffer) WriteByte(c byte) error {
	var buf [1]byte
	buf[0] = c
	_, err := b.Write(buf[:])
	return err
}

// Bytes returns all buffered bytes.
func (b *ArenaBuffer) Bytes() []byte {
	if b.totalLen == 0 {
		return nil
	}
	if b.blockCount == 1 && b.chunk == nil {
		return b.blocks[0][:b.blockLens[0]]
	}
	dst := make([]byte, b.totalLen)
	pos := 0
	for i := 0; i < b.blockCount; i++ {
		used := b.blockLens[i]
		if used > 0 {
			copy(dst[pos:], b.blocks[i][:used])
			pos += used
		}
	}
	if b.chunk != nil && b.chunk.Len() > 0 {
		copy(dst[pos:], b.chunk.Bytes())
	}
	return dst
}

// VisitChunks calls fn for each chunk slice in order.
// If fn returns false, iteration stops early.
func (b *ArenaBuffer) VisitChunks(fn func(chunk []byte) bool) {
	for i := 0; i < b.blockCount; i++ {
		used := b.blockLens[i]
		if used > 0 {
			if !fn(b.blocks[i][:used]) {
				return
			}
		}
	}
	if b.chunk != nil && b.chunk.Len() > 0 {
		b.chunk.VisitChunks(fn)
	}
}

// AppendChunks appends all unread block slices to dst without copying data
// and returns the extended slice. The appended slices point directly into
// internal blocks (zero-copy views).
func (b *ArenaBuffer) AppendChunks(dst [][]byte) [][]byte {
	b.VisitChunks(func(chunk []byte) bool {
		dst = append(dst, chunk)
		return true
	})
	return dst
}

// Chunks returns a slice of byte slices representing all buffered blocks.
func (b *ArenaBuffer) Chunks() [][]byte {
	if b.totalLen == 0 {
		return nil
	}
	dst := make([][]byte, 0, b.blockCount+1)
	return b.AppendChunks(dst)
}

// CopyTo copies up to len(dst) buffered bytes into dst without modifying the buffer.
// It returns the number of bytes copied.
func (b *ArenaBuffer) CopyTo(dst []byte) int {
	if len(dst) == 0 || b.totalLen == 0 {
		return 0
	}
	written := 0
	b.VisitChunks(func(chunk []byte) bool {
		take := copy(dst[written:], chunk)
		written += take
		return written < len(dst)
	})
	return written
}

// WriteTo writes all buffered bytes to w.
func (b *ArenaBuffer) WriteTo(w io.Writer) (int64, error) {
	var total int64
	for i := 0; i < b.blockCount; i++ {
		used := b.blockLens[i]
		if used > 0 {
			n, err := w.Write(b.blocks[i][:used])
			total += int64(n)
			if err != nil {
				return total, err
			}
		}
	}
	if b.chunk != nil && b.chunk.Len() > 0 {
		n, err := b.chunk.WriteTo(w)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// Reset clears the buffer content while releasing blocks back to the arena.
func (b *ArenaBuffer) Reset() {
	if b.arena != nil {
		for i := 0; i < b.blockCount; i++ {
			id := b.blockIDs[i]
			if id >= 0 {
				b.arena.FreeBlock(int(id))
				b.blockIDs[i] = -1
			}
			b.blocks[i] = nil
			b.blockLens[i] = 0
		}
	}
	b.blockCount = 0
	b.totalLen = 0
	if b.chunk != nil {
		ReleaseBuffer(b.chunk)
		b.chunk = nil
	}
}

// Close releases all held resources back to the arena and chunk pools.
func (b *ArenaBuffer) Close() {
	b.Reset()
}
