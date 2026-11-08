// Package chunkbuffer provides a byte buffer that stores its contents in
// fixed-size blocks instead of one contiguous slice.
//
// The point is not raw speed: for small and medium payloads bytes.Buffer is
// faster and always will be. The point is what happens when a buffer has to
// grow to hundreds of megabytes. bytes.Buffer grows by doubling, so writing
// 64 MiB transiently allocates ~127 MiB (see TestPeakAllocation). Buffer never
// reallocates: it appends blocks, so its total allocation stays at ~1x, and it
// never needs one huge contiguous allocation.
//
// Use Buffer when you expect large payloads, or when a single huge allocation
// is undesirable. Otherwise, use bytes.Buffer.
//
// A Buffer works as a zero value and is not safe for concurrent use.
//
// Parts of the API, including several error strings and doc comments, are
// derived from the Go standard library's bytes.Buffer:
//
//	Copyright 2009 The Go Authors. All rights reserved.
//	Use of this source code is governed by a BSD-style license.
package chunkbuffer

import (
	"errors"
	"io"
	"math/bits"
	"sync"
)

var (
	_ io.Reader       = (*Buffer)(nil)
	_ io.Writer       = (*Buffer)(nil)
	_ io.ByteReader   = (*Buffer)(nil)
	_ io.ByteWriter   = (*Buffer)(nil)
	_ io.StringWriter = (*Buffer)(nil)
	_ io.ReaderFrom   = (*Buffer)(nil)
	_ io.WriterTo     = (*Buffer)(nil)
)

// DefaultChunkSize is the block size used when none is given to NewBuffer.
const DefaultChunkSize = 8 << 10

// ErrTooLarge is panicked when a write would exceed the limit set by
// SetMaxSize. Like bytes.Buffer, the affected methods panic rather than
// return it, because exceeding a limit you configured is a programming error.
var ErrTooLarge = errors.New("chunkbuffer: too large")

// ErrNegativeRead is panicked when an io.Reader returns a negative count.
var ErrNegativeRead = errors.New("chunkbuffer: reader returned negative count from Read")

// Chunk recycling uses one sync.Pool per size class. Class i holds chunks
// whose capacity is exactly DefaultChunkSize<<i, from DefaultChunkSize (class
// 0) up to DefaultChunkSize<<maxPoolClass. Per-class pooling keeps every pool
// homogeneous — a buffer never inherits a block much larger than its own
// chunk size — while still letting custom chunk sizes recycle their blocks
// instead of reallocating them on every drain/refill cycle. Chunks larger
// than the biggest class are allocated exactly and never pooled.
const (
	maxPoolClass     = 10
	defaultChunkBits = 13 // log2(DefaultChunkSize) where DefaultChunkSize is 8 << 10
)

var chunkPools [maxPoolClass + 1]sync.Pool

func init() {
	for i := range chunkPools {
		classCap := DefaultChunkSize << i
		chunkPools[i].New = func() any {
			return &chunk{B: make([]byte, 0, classCap)}
		}
	}
}

// chunk is one fixed-size block.
type chunk struct {
	B []byte
}

// poolClass returns the index of the smallest pool class whose chunks can
// hold size bytes (class capacity >= size), or -1 if size exceeds the largest
// class. It runs in O(1) time using bit manipulation.
func poolClass(size int) int {
	if size <= DefaultChunkSize {
		return 0
	}
	if size > DefaultChunkSize<<maxPoolClass {
		return -1
	}
	return bits.Len(uint(size-1)) - defaultChunkBits
}

// getChunk returns an empty block with capacity at least size. For a pooled
// class the capacity is exactly the class capacity, which may be larger than
// size; callers must not assume cap(c.B) == size.
func getChunk(size int) *chunk {
	if size < 1 {
		size = DefaultChunkSize
	}
	if i := poolClass(size); i >= 0 {
		return chunkPools[i].Get().(*chunk)
	}
	return &chunk{B: make([]byte, 0, size)}
}

// putChunk returns the block to its size class's pool. Blocks whose capacity
// is not exactly a class capacity are dropped (left to the GC); since blocks
// are always allocated at their class capacity, this only affects oversized
// blocks, which never enter a pool.
//
// It does not wipe the backing array. That is safe because of an invariant
// enforced by construction and covered by TestNoMemoryDisclosure: every code
// path that exposes block contents (Read, WriteTo, Bytes) bounds its slice by
// len(c.B), and len(c.B) only ever covers bytes the current buffer wrote.
// Stale bytes live strictly beyond len and are therefore never observable.
//
// Wiping here measured ~20% slower on the round-trip benchmark, so the
// guarantee is structural rather than defensive. bytes.Buffer also simply
// reslices to [:0].
func putChunk(c *chunk) {
	n := cap(c.B)
	i := poolClass(n)
	if i < 0 || DefaultChunkSize<<i != n {
		return
	}
	c.B = c.B[:0]
	chunkPools[i].Put(c)
}

// Buffer is a variable-sized byte buffer backed by a queue of blocks.
//
// The zero value is an empty buffer ready to use.
//
// The live blocks are chunks[head:]. All of them are completely full except
// the last one, which is where writes land. This is the invariant that keeps
// the arithmetic simple: reads start at the head block, writes at the tail.
type Buffer struct {
	chunks []*chunk // live blocks are chunks[head:]; earlier entries are nil
	free   []*chunk // cached blocks retained across Reset for fast refilling
	head   int      // index of the first live block
	r      int      // read offset within chunks[head]
	n      int      // unread bytes, i.e. Len()

	chunkSize int   // block size; 0 means DefaultChunkSize
	maxSize   int64 // limit on Len(); 0 means unlimited
}

// NewBuffer returns an empty Buffer. The optional chunkSize sets the block
// size; values below 1 (or omitting it) fall back to DefaultChunkSize.
func NewBuffer(chunkSize ...int) *Buffer {
	b := &Buffer{}
	if len(chunkSize) > 0 {
		b.chunkSize = chunkSize[0]
	}
	return b
}

// size returns the effective block size for this buffer.
func (b *Buffer) size() int {
	if b.chunkSize < 1 {
		return DefaultChunkSize
	}
	return b.chunkSize
}

// SetMaxSize limits the number of buffered bytes. Once Len() would exceed n,
// writes panic with ErrTooLarge. Zero (the default) means no limit.
func (b *Buffer) SetMaxSize(n int64) {
	b.maxSize = n
}

// tail returns the block writes should append to, creating one if the buffer
// is empty or the last block is full. The returned block always has room for
// at least one byte.
func (b *Buffer) tail() *chunk {
	if len(b.chunks) > b.head {
		c := b.chunks[len(b.chunks)-1]
		if len(c.B) < b.size() {
			return c
		}
	}
	var c *chunk
	if n := len(b.free); n > 0 {
		c = b.free[n-1]
		b.free = b.free[:n-1]
	} else {
		c = getChunk(b.size())
	}
	b.chunks = append(b.chunks, c)
	return c
}

// Write appends p to the buffer. It never returns an error; the error result
// exists to satisfy io.Writer. If a limit is set and adding p would exceed it,
// Write panics with ErrTooLarge.
func (b *Buffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.maxSize > 0 && int64(b.n)+int64(len(p)) > b.maxSize {
		panic(ErrTooLarge)
	}
	total := len(p)
	size := b.size()

	// Fill available room in the existing tail chunk, if any.
	if len(b.chunks) > b.head {
		c := b.chunks[len(b.chunks)-1]
		if room := size - len(c.B); room > 0 {
			take := len(p)
			if take > room {
				take = room
			}
			curLen := len(c.B)
			c.B = c.B[:curLen+take]
			copy(c.B[curLen:], p[:take])
			b.n += take
			p = p[take:]
		}
	}

	// Any remaining bytes go into fresh blocks.
	for len(p) > 0 {
		var c *chunk
		if n := len(b.free); n > 0 {
			c = b.free[n-1]
			b.free = b.free[:n-1]
		} else {
			c = getChunk(size)
		}
		b.chunks = append(b.chunks, c)

		take := len(p)
		if take > size {
			take = size
		}
		c.B = c.B[:take]
		copy(c.B, p[:take])
		b.n += take
		p = p[take:]
	}
	return total, nil
}

// WriteString appends the contents of s to the buffer. It implements
// io.StringWriter and never returns an error. If a limit is set and adding s
// would exceed it, WriteString panics with ErrTooLarge.
func (b *Buffer) WriteString(s string) (int, error) {
	if len(s) == 0 {
		return 0, nil
	}
	if b.maxSize > 0 && int64(b.n)+int64(len(s)) > b.maxSize {
		panic(ErrTooLarge)
	}
	total := len(s)
	size := b.size()

	// Fill available room in the existing tail chunk, if any.
	if len(b.chunks) > b.head {
		c := b.chunks[len(b.chunks)-1]
		if room := size - len(c.B); room > 0 {
			take := len(s)
			if take > room {
				take = room
			}
			curLen := len(c.B)
			c.B = c.B[:curLen+take]
			copy(c.B[curLen:], s[:take])
			b.n += take
			s = s[take:]
		}
	}

	// Any remaining bytes go into fresh blocks.
	for len(s) > 0 {
		var c *chunk
		if n := len(b.free); n > 0 {
			c = b.free[n-1]
			b.free = b.free[:n-1]
		} else {
			c = getChunk(size)
		}
		b.chunks = append(b.chunks, c)

		take := len(s)
		if take > size {
			take = size
		}
		c.B = c.B[:take]
		copy(c.B, s[:take])
		b.n += take
		s = s[take:]
	}
	return total, nil
}

// WriteByte appends the byte c to the buffer. It implements io.ByteWriter and
// never returns an error. If a limit is set and adding one byte would exceed
// it, WriteByte panics with ErrTooLarge.
func (b *Buffer) WriteByte(c byte) error {
	if b.maxSize > 0 && int64(b.n)+1 > b.maxSize {
		panic(ErrTooLarge)
	}
	if len(b.chunks) > b.head {
		tail := b.chunks[len(b.chunks)-1]
		if len(tail.B) < b.size() {
			tail.B = append(tail.B, c)
			b.n++
			return nil
		}
	}
	return b.writeByteSlow(c)
}

func (b *Buffer) writeByteSlow(c byte) error {
	chunk := b.tail()
	chunk.B = append(chunk.B, c)
	b.n++
	return nil
}

// Read reads the next len(p) bytes from the buffer or until the buffer is
// drained. The return value n is the number of bytes read. If the buffer has
// no data to return, err is io.EOF (unless len(p) is zero); otherwise it is
// nil. A non-zero n always comes with a nil error, matching bytes.Buffer.
func (b *Buffer) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.n == 0 {
		return 0, io.EOF
	}
	n := 0
	// Invariant: 0 <= b.r < len(chunks[head].B) whenever n > 0, because a block
	// is dropped as soon as it is exhausted. So avail is always positive here.
	for len(p) > 0 && b.n > 0 {
		c := b.chunks[b.head]
		avail := len(c.B) - b.r
		take := len(p)
		if take > avail {
			take = avail
		}
		copy(p, c.B[b.r:b.r+take])
		b.r += take
		b.n -= take
		n += take
		p = p[take:]
		if b.r >= len(c.B) {
			b.dropHead()
		}
	}
	return n, nil
}

// ReadByte reads and returns the next byte from the buffer. It implements
// io.ByteReader. If no byte is available, it returns error io.EOF.
func (b *Buffer) ReadByte() (byte, error) {
	if b.n == 0 {
		return 0, io.EOF
	}
	c := b.chunks[b.head]
	if b.r < len(c.B)-1 {
		ch := c.B[b.r]
		b.r++
		b.n--
		return ch, nil
	}
	return b.readByteSlow()
}

func (b *Buffer) readByteSlow() (byte, error) {
	c := b.chunks[b.head]
	ch := c.B[b.r]
	b.r++
	b.n--
	if b.r >= len(c.B) {
		b.dropHead()
	}
	return ch, nil
}

// dropHead releases the leading block and advances the window. Releasing on
// drain is what keeps a long-lived streaming buffer at constant memory; the
// original implementation only ever appended.
func (b *Buffer) dropHead() {
	putChunk(b.chunks[b.head])
	b.chunks[b.head] = nil
	b.head++
	b.r = 0
	if b.head == len(b.chunks) {
		b.chunks = b.chunks[:0]
		b.head = 0
		return
	}
	b.compact()
}

// compact reclaims the consumed prefix of the block slice once it grows large
// enough to justify the copy, so a long stream does not leak slice entries.
func (b *Buffer) compact() {
	if b.head < compactThreshold {
		return
	}
	n := copy(b.chunks, b.chunks[b.head:])
	for i := n; i < len(b.chunks); i++ {
		b.chunks[i] = nil
	}
	b.chunks = b.chunks[:n]
	b.head = 0
}

// compactThreshold is how many consumed entries accumulate before the block
// slice is compacted.
const compactThreshold = 64

// ReadFrom reads data from r until EOF and appends it to the buffer, growing
// the buffer as needed. The return value n is the number of bytes read. Any
// error except io.EOF encountered during the read is also returned. If the
// buffer becomes too large, ReadFrom panics with ErrTooLarge.
func (b *Buffer) ReadFrom(r io.Reader) (n int64, err error) {
	size := b.size()
	for {
		if b.maxSize > 0 && int64(b.n) >= b.maxSize {
			panic(ErrTooLarge)
		}
		c := b.tail()
		start := len(c.B)
		room := size - start
		if b.maxSize > 0 {
			if allowance := b.maxSize - int64(b.n); int64(room) > allowance {
				room = int(allowance)
			}
		}
		m, e := r.Read(c.B[start : start+room])
		if m < 0 {
			panic(ErrNegativeRead)
		}
		if m > room {
			panic("chunkbuffer: reader returned more bytes than requested")
		}
		c.B = c.B[:start+m]
		b.n += m
		n += int64(m)
		if e == io.EOF {
			return n, nil // EOF, so return nil explicitly
		}
		if e != nil {
			return n, e
		}
	}
}

// WriteTo writes data to w until the buffer is drained or an error occurs. The
// return value n is the number of bytes written; it always fits into an int,
// but it is int64 to match the io.WriterTo interface. Any error encountered
// during the write is also returned.
func (b *Buffer) WriteTo(w io.Writer) (n int64, err error) {
	remaining := b.n
	for remaining > 0 {
		c := b.chunks[b.head]
		avail := len(c.B) - b.r
		take := remaining
		if take > avail {
			take = avail
		}
		m, e := w.Write(c.B[b.r : b.r+take])
		n += int64(m)
		b.r += m
		b.n -= m
		remaining -= m
		if b.r >= len(c.B) {
			b.dropHead()
		}
		if e != nil {
			return n, e
		}
		if m != take {
			return n, io.ErrShortWrite
		}
	}
	return n, nil
}

// Bytes returns the unread portion of the buffer as a new slice.
//
// Unlike bytes.Buffer, the result is a copy: mutating it does not affect the
// buffer, and it stays valid after the buffer is reused. That costs one
// allocation; use WriteTo or Read to avoid it.
func (b *Buffer) Bytes() []byte {
	dst := make([]byte, b.n)
	written := 0
	for i := b.head; i < len(b.chunks) && written < b.n; i++ {
		off := 0
		if i == b.head {
			off = b.r
		}
		written += copy(dst[written:], b.chunks[i].B[off:])
	}
	return dst
}

// String returns the contents of the unread portion of the buffer as a string.
func (b *Buffer) String() string { return string(b.Bytes()) }

// Len returns the number of bytes of the unread portion of the buffer.
func (b *Buffer) Len() int { return b.n }

// Grow grows the buffer's capacity, if necessary, to guarantee space for
// another n bytes. After Grow(n), at least n bytes can be written to the
// buffer without another allocation.
// If n is negative, Grow panics.
// If the buffer cannot grow to accommodate n bytes, it panics with ErrTooLarge.
func (b *Buffer) Grow(n int) {
	if n < 0 {
		panic("chunkbuffer.Buffer.Grow: negative count")
	}
	if b.maxSize > 0 && int64(b.n)+int64(n) > b.maxSize {
		panic(ErrTooLarge)
	}
	size := b.size()
	room := 0
	if len(b.chunks) > b.head {
		room = size - len(b.chunks[len(b.chunks)-1].B)
	}
	room += len(b.free) * size
	if n <= room {
		return
	}
	needed := n - room
	blocksNeeded := (needed + size - 1) / size

	totalChunks := len(b.chunks) + len(b.free) + blocksNeeded
	if cap(b.chunks) < totalChunks {
		newChunks := make([]*chunk, len(b.chunks), totalChunks)
		copy(newChunks, b.chunks)
		b.chunks = newChunks
	}

	for i := 0; i < blocksNeeded; i++ {
		b.free = append(b.free, getChunk(size))
	}
}

// Cap returns the total capacity of the blocks currently held by the buffer.
func (b *Buffer) Cap() int {
	total := 0
	for i := b.head; i < len(b.chunks); i++ {
		total += cap(b.chunks[i].B)
	}
	for _, c := range b.free {
		total += cap(c.B)
	}
	return total
}

// Reset discards the buffer's contents while keeping the blocks for reuse, so
// that a reset buffer can be refilled without allocating. Call Close to
// release the memory instead.
//
// Reset retains every block the buffer held, so the buffer keeps its peak
// memory footprint: a buffer that once held 64 MiB still holds 64 MiB of
// blocks after Reset, and — unlike blocks sitting in the shared pool — that
// memory is not reclaimed by the GC while the buffer is alive. For a
// long-lived buffer fed by spiky payloads, call Close after an oversized
// payload to release the blocks.
func (b *Buffer) Reset() {
	live := b.chunks[b.head:]
	if len(live) == 0 {
		b.chunks = b.chunks[:0]
		b.head, b.r, b.n = 0, 0, 0
		return
	}
	keep := live[0]
	keep.B = keep.B[:0]
	for _, c := range live[1:] {
		c.B = c.B[:0]
		b.free = append(b.free, c)
	}
	b.chunks = b.chunks[:0]
	b.chunks = append(b.chunks, keep)
	b.head, b.r, b.n = 0, 0, 0
}

// Close releases every block back to the pool, leaving the buffer empty and
// reusable. It is idempotent.
func (b *Buffer) Close() {
	for i := b.head; i < len(b.chunks); i++ {
		putChunk(b.chunks[i])
		b.chunks[i] = nil
	}
	b.chunks = b.chunks[:0]
	for _, c := range b.free {
		putChunk(c)
	}
	b.free = b.free[:0]
	b.head, b.r, b.n = 0, 0, 0
}

// LiveChunkCount returns the number of blocks currently holding unread data.
func (b *Buffer) LiveChunkCount() int {
	if b.n == 0 || b.head >= len(b.chunks) {
		return 0
	}
	return len(b.chunks) - b.head
}

// VisitChunks invokes fn for each unread block slice in order.
// If fn returns false, iteration stops early.
// The passed slices are zero-copy views into the internal blocks; callers must
// not modify their contents or retain them past subsequent mutations of b.
func (b *Buffer) VisitChunks(fn func(chunk []byte) bool) {
	remaining := b.n
	for i := b.head; i < len(b.chunks) && remaining > 0; i++ {
		c := b.chunks[i]
		offset := 0
		if i == b.head {
			offset = b.r
		}
		avail := len(c.B) - offset
		if avail <= 0 {
			continue
		}
		take := remaining
		if take > avail {
			take = avail
		}
		slice := c.B[offset : offset+take]
		if len(slice) > 0 {
			if !fn(slice) {
				return
			}
		}
		remaining -= take
	}
}

// AppendChunks appends all unread block slices to dst without copying data
// and returns the extended slice. The appended slices point directly into
// internal blocks (zero-copy views).
func (b *Buffer) AppendChunks(dst [][]byte) [][]byte {
	b.VisitChunks(func(chunk []byte) bool {
		dst = append(dst, chunk)
		return true
	})
	return dst
}

// Chunks returns a slice of byte slices representing all unread blocks.
// The returned slices point directly into internal blocks (zero-copy views).
func (b *Buffer) Chunks() [][]byte {
	if b.n == 0 {
		return nil
	}
	count := b.LiveChunkCount()
	if count <= 0 {
		return nil
	}
	dst := make([][]byte, 0, count)
	return b.AppendChunks(dst)
}

// CopyTo copies up to len(dst) unread bytes from b into dst without advancing
// the read position. It returns the number of bytes copied.
func (b *Buffer) CopyTo(dst []byte) int {
	if len(dst) == 0 || b.n == 0 {
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

var bufferPool sync.Pool

// AcquireBuffer retrieves a recycled Buffer from the internal pool.
// The returned buffer is empty, reset, and ready for immediate use.
// Call ReleaseBuffer when done to recycle both its blocks and the Buffer struct.
func AcquireBuffer(chunkSize ...int) *Buffer {
	v := bufferPool.Get()
	if v == nil {
		return NewBuffer(chunkSize...)
	}
	b := v.(*Buffer)
	if len(chunkSize) > 0 {
		b.chunkSize = chunkSize[0]
	} else {
		b.chunkSize = DefaultChunkSize
	}
	b.maxSize = 0
	b.head, b.r, b.n = 0, 0, 0
	return b
}

// defaultMaxRetainedCap defines the capacity ceiling (128 KiB) for retaining blocks on release.
const defaultMaxRetainedCap = 128 * 1024

// ReleaseBuffer returns b to the internal pool.
// Buffers whose capacity does not exceed defaultMaxRetainedCap are reset
// (preserving their blocks in b.free for instant reuse without pool thrashing);
// oversized buffers are closed to return blocks to the size-class pools.
func ReleaseBuffer(b *Buffer) {
	if b == nil {
		return
	}
	if b.Cap() > defaultMaxRetainedCap {
		b.Close()
	} else {
		b.Reset()
	}
	bufferPool.Put(b)
}
