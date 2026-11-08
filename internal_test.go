package chunkbuffer

import (
	"bytes"
	"io"
	"testing"
)

// checkInvariants validates the structural invariants the implementation
// relies on. It is called from tests only.
func checkInvariants(t *testing.T, b *Buffer) {
	t.Helper()

	if b.head > len(b.chunks) {
		t.Fatalf("head %d > len(chunks) %d", b.head, len(b.chunks))
	}
	if b.r < 0 {
		t.Fatalf("r = %d", b.r)
	}

	size := b.size()
	live := b.chunks[b.head:]

	// Every live block must be present, and all but the last must be full.
	for i, c := range live {
		if c == nil {
			t.Fatalf("live block %d is nil", i)
		}
		if i < len(live)-1 && len(c.B) != size {
			t.Fatalf("non-tail block %d has len %d, want %d", i, len(c.B), size)
		}
		if len(c.B) > size {
			t.Fatalf("block %d overflows block size: len %d > %d", i, len(c.B), size)
		}
		if i == 0 && b.r > len(c.B) {
			t.Fatalf("r = %d exceeds head block len %d", b.r, len(c.B))
		}
	}

	// Every cached free block must have len == 0.
	for i, c := range b.free {
		if c == nil {
			t.Fatalf("free block %d is nil", i)
		}
		if len(c.B) != 0 {
			t.Fatalf("free block %d has len %d, want 0", i, len(c.B))
		}
	}

	// The sum of live bytes minus the consumed prefix must equal Len().
	sum := 0
	for _, c := range live {
		sum += len(c.B)
	}
	if got := sum - b.r; got != b.n {
		t.Fatalf("byte count = %d (sum %d - r %d), want Len %d", got, sum, b.r, b.n)
	}

	// Bytes() must agree with Len().
	if got := b.Bytes(); len(got) != b.n {
		t.Fatalf("Bytes() len = %d, want Len %d", len(got), b.n)
	}
}

// TestResetWithConsumedPrefix covers Reset after reads have advanced head,
// which must compact the block slice rather than leak the consumed prefix.
func TestResetWithConsumedPrefix(t *testing.T) {
	b := NewBuffer(4)
	defer b.Close()

	for i := 0; i < 100; i++ {
		b.Write([]byte("abcd"))
	}
	// Consume 90 blocks, leaving head > 0.
	io.CopyN(io.Discard, b, 90*4)
	if b.head == 0 {
		t.Fatalf("expected head > 0 after consuming, got %d", b.head)
	}
	checkInvariants(t, b)

	b.Reset()
	if b.head != 0 {
		t.Fatalf("Reset left head = %d, want 0", b.head)
	}
	if b.Len() != 0 || len(b.Bytes()) != 0 {
		t.Fatalf("Reset left data behind")
	}
	checkInvariants(t, b)

	// Capacity must survive Reset for the streaming case.
	if b.Cap() == 0 {
		t.Fatalf("Reset released capacity; expected reuse")
	}

	b.Write([]byte("xyz"))
	if b.String() != "xyz" {
		t.Fatalf("String() = %q, want %q", b.String(), "xyz")
	}
	checkInvariants(t, b)
}

// TestCompactionLongStream forces the consumed prefix past the compaction
// threshold so compact() actually runs, and checks data stays correct.
func TestCompactionLongStream(t *testing.T) {
	b := NewBuffer(8)
	defer b.Close()

	var want bytes.Buffer
	for i := 0; i < 500; i++ {
		payload := bytes.Repeat([]byte{byte('a' + i%26)}, 8)
		b.Write(payload)
		want.Write(payload)
		checkInvariants(t, b)

		if i%3 == 0 {
			got := make([]byte, 8)
			if _, err := b.Read(got); err != nil {
				t.Fatalf("Read: %v", err)
			}
			want.Next(8)
		}
	}

	if got := b.Bytes(); !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("content mismatch after long stream: got %d bytes, want %d",
			len(got), want.Len())
	}
	// Compaction keeps the consumed prefix bounded, so head must never grow
	// without limit; it is recycled once it passes the threshold.
	if b.head >= 64 {
		t.Fatalf("head grew to %d; compaction is not reclaiming the prefix (chunks %d)",
			b.head, len(b.chunks))
	}
	live := len(b.chunks) - b.head
	if live > 512 {
		t.Fatalf("live blocks = %d, expected the stream to stay bounded", live)
	}
}

// TestWriteAcrossManyBlocks exercises writeBlock's "block is full, advance"
// path repeatedly with awkward sizes.
func TestWriteAcrossManyBlocks(t *testing.T) {
	for _, tc := range []struct{ chunkSize, writeLen int }{
		{1, 9}, {2, 9}, {3, 10}, {5, 16}, {7, 100}, {8, 8}, {16, 33},
	} {
		b := NewBuffer(tc.chunkSize)
		payload := bytes.Repeat([]byte("abcdefgh"), tc.writeLen)
		for i := 0; i < 3; i++ {
			n, err := b.Write(payload)
			if err != nil || n != len(payload) {
				t.Fatalf("cs=%d: Write = (%d, %v)", tc.chunkSize, n, err)
			}
			checkInvariants(t, b)
		}
		if got := b.Bytes(); !bytes.Equal(got, bytes.Repeat(payload, 3)) {
			t.Fatalf("cs=%d: content mismatch", tc.chunkSize)
		}
		b.Close()
	}
}

// TestLargeChunkSizeFallback covers nextBlock growing a pooled block when the
// pool hands back one smaller than the buffer's size.
func TestLargeChunkSizeFallback(t *testing.T) {
	// A default-sized block is in the pool; this buffer needs much bigger.
	b := NewBuffer(1 << 16)
	defer b.Close()
	b.Write(bytes.Repeat([]byte("x"), 3<<16))

	for i := b.head; i < len(b.chunks); i++ {
		if c := b.chunks[i]; c != nil && cap(c.B) < b.size() {
			t.Fatalf("block %d has cap %d < size %d", i, cap(c.B), b.size())
		}
	}
	if b.Len() != 3<<16 {
		t.Fatalf("Len() = %d, want %d", b.Len(), 3<<16)
	}
	checkInvariants(t, b)
}

// TestReadEmptyZeroLen covers the len(p)==0 early return on both an empty and
// a non-empty buffer.
func TestReadEmptyZeroLen(t *testing.T) {
	var b Buffer
	defer b.Close()

	if n, err := b.Read(nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) on empty = (%d, %v), want (0, nil)", n, err)
	}
	b.Write([]byte("data"))
	if n, err := b.Read(nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) with data = (%d, %v), want (0, nil)", n, err)
	}
	if b.Len() != 4 {
		t.Fatalf("Read(nil) consumed data: Len = %d", b.Len())
	}
}

// TestEmptyBufferOperations covers no-op paths on a never-written buffer.
func TestEmptyBufferOperations(t *testing.T) {
	var b Buffer
	defer b.Close()

	if n, err := b.WriteTo(io.Discard); n != 0 || err != nil {
		t.Fatalf("WriteTo on empty = (%d, %v)", n, err)
	}
	b.Reset()
	if b.Cap() != 0 {
		t.Fatalf("Cap() = %d, want 0", b.Cap())
	}
	n, err := b.ReadFrom(bytes.NewReader(nil))
	if n != 0 || err != nil {
		t.Fatalf("ReadFrom(empty) = (%d, %v)", n, err)
	}
	checkInvariants(t, &b)
}

// TestReadFromRespectsMaxSizePartially covers the clamp branch where the
// limit allows one more partial Read before panicking.
func TestReadFromRespectsMaxSizePartially(t *testing.T) {
	b := NewBuffer(4)
	defer b.Close()
	b.SetMaxSize(6)

	defer func() {
		if r := recover(); r != ErrTooLarge {
			t.Fatalf("expected ErrTooLarge panic, got %v", r)
		}
		if b.Len() != 6 {
			t.Fatalf("Len() = %d after hitting limit, want 6", b.Len())
		}
		if b.String() != "abcdef" {
			t.Fatalf("data = %q, want %q", b.String(), "abcdef")
		}
	}()

	b.ReadFrom(bytes.NewReader([]byte("abcdefghij")))
	t.Fatalf("expected ReadFrom to panic once the limit was exceeded")
}

// TestPoolClasses covers the size-class helpers directly, including the
// oversized paths that the public API only reaches with huge chunk sizes.
func TestPoolClasses(t *testing.T) {
	if got := poolClass(1); got != 0 {
		t.Fatalf("poolClass(1) = %d, want 0", got)
	}
	if got := poolClass(DefaultChunkSize); got != 0 {
		t.Fatalf("poolClass(DefaultChunkSize) = %d, want 0", got)
	}
	if got := poolClass(DefaultChunkSize + 1); got != 1 {
		t.Fatalf("poolClass(DefaultChunkSize+1) = %d, want 1", got)
	}
	if got := poolClass(DefaultChunkSize << maxPoolClass); got != maxPoolClass {
		t.Fatalf("poolClass(max) = %d, want %d", got, maxPoolClass)
	}
	if got := poolClass(DefaultChunkSize<<maxPoolClass + 1); got != -1 {
		t.Fatalf("poolClass(oversized) = %d, want -1", got)
	}

	// getChunk clamps invalid sizes to the default class.
	c := getChunk(0)
	if cap(c.B) != DefaultChunkSize {
		t.Fatalf("getChunk(0) cap = %d, want %d", cap(c.B), DefaultChunkSize)
	}
	putChunk(c)

	// Oversized blocks are allocated exactly and dropped by putChunk.
	big := getChunk(DefaultChunkSize<<maxPoolClass + 1)
	if cap(big.B) != DefaultChunkSize<<maxPoolClass+1 {
		t.Fatalf("oversized getChunk cap = %d, want %d", cap(big.B), DefaultChunkSize<<maxPoolClass+1)
	}
	putChunk(big) // must not panic and must not enter a pool
}

// TestOversizedChunkSize exercises a buffer whose chunk size exceeds the
// largest pool class end to end: blocks are allocated exactly, never pooled,
// and round-trip correctly.
func TestOversizedChunkSize(t *testing.T) {
	const size = DefaultChunkSize<<maxPoolClass + 4096 // just above the biggest class
	b := NewBuffer(size)
	defer b.Close()

	payload := bytes.Repeat([]byte("o"), size+1) // spans two oversized blocks
	if _, err := b.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if b.Len() != len(payload) {
		t.Fatalf("Len() = %d, want %d", b.Len(), len(payload))
	}
	for i := b.head; i < len(b.chunks); i++ {
		if got := cap(b.chunks[i].B); got != size {
			t.Fatalf("block %d cap = %d, want exact size %d", i, got, size)
		}
	}
	var out bytes.Buffer
	if _, err := b.WriteTo(&out); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("round-trip mismatch")
	}
	checkInvariants(t, b)
}
