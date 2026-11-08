package chunkbuffer

import (
	"bytes"
	"testing"
)

func TestSlabArena(t *testing.T) {
	arena := NewSlabArena()

	if arena.InUse() != 0 {
		t.Fatalf("expected InUse=0, got %d", arena.InUse())
	}
	if arena.Available() != ArenaBlockCount {
		t.Fatalf("expected Available=%d, got %d", ArenaBlockCount, arena.Available())
	}

	// 1. Allocate all 64 blocks
	var blockIDs [ArenaBlockCount]int
	for i := 0; i < ArenaBlockCount; i++ {
		blk, id, ok := arena.AllocBlock()
		if !ok || id != i {
			t.Fatalf("AllocBlock %d failed: id=%d ok=%v", i, id, ok)
		}
		if len(blk) != ArenaBlockSize {
			t.Fatalf("AllocBlock %d length mismatch: %d", i, len(blk))
		}
		blockIDs[i] = id
	}

	if arena.InUse() != ArenaBlockCount {
		t.Fatalf("expected InUse=%d, got %d", ArenaBlockCount, arena.InUse())
	}
	if arena.Available() != 0 {
		t.Fatalf("expected Available=0, got %d", arena.Available())
	}

	// 2. 65th allocation must fail
	_, _, ok := arena.AllocBlock()
	if ok {
		t.Fatalf("expected 65th allocation to fail")
	}

	// 3. Free block 5 and reallocate
	arena.FreeBlock(5)
	if arena.InUse() != ArenaBlockCount-1 {
		t.Fatalf("expected InUse=%d after free, got %d", ArenaBlockCount-1, arena.InUse())
	}
	blk5, id5, ok5 := arena.AllocBlock()
	if !ok5 || id5 != 5 || len(blk5) != ArenaBlockSize {
		t.Fatalf("expected reallocating block 5, got id=%d ok=%v", id5, ok5)
	}

	// 4. Reset
	arena.Reset()
	if arena.InUse() != 0 {
		t.Fatalf("expected InUse=0 after Reset, got %d", arena.InUse())
	}
}

func TestArenaBuffer(t *testing.T) {
	arena := NewSlabArena()
	buf := NewArenaBuffer(arena)
	defer buf.Close()

	if buf.Len() != 0 {
		t.Fatalf("expected Len=0, got %d", buf.Len())
	}
	if buf.Bytes() != nil {
		t.Fatalf("expected Bytes=nil for empty buffer")
	}
	if buf.Chunks() != nil {
		t.Fatalf("expected Chunks=nil for empty buffer")
	}

	// 1. Multi-block write (64 KB = 2 blocks)
	payload := make([]byte, 64*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	n, err := buf.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("Write failed: n=%d, err=%v", n, err)
	}
	if buf.Len() != len(payload) {
		t.Fatalf("Len() mismatch: got %d, want %d", buf.Len(), len(payload))
	}
	if arena.InUse() != 2 {
		t.Fatalf("expected 2 arena blocks in use, got %d", arena.InUse())
	}

	// Verify content
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Fatalf("Bytes() content mismatch")
	}

	// 2. Chunks and VisitChunks
	chunks := buf.Chunks()
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}

	var gathered []byte
	buf.VisitChunks(func(chunk []byte) bool {
		gathered = append(gathered, chunk...)
		return true
	})
	if !bytes.Equal(gathered, payload) {
		t.Fatalf("VisitChunks content mismatch")
	}

	// 3. CopyTo
	dst := make([]byte, len(payload))
	copied := buf.CopyTo(dst)
	if copied != len(payload) || !bytes.Equal(dst, payload) {
		t.Fatalf("CopyTo mismatch: copied=%d", copied)
	}

	// 4. WriteTo
	var out bytes.Buffer
	wCount, wErr := buf.WriteTo(&out)
	if wErr != nil || wCount != int64(len(payload)) {
		t.Fatalf("WriteTo failed: %v", wErr)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("WriteTo content mismatch")
	}

	// 5. Reset releases blocks
	buf.Reset()
	if buf.Len() != 0 {
		t.Fatalf("Len() not 0 after Reset: %d", buf.Len())
	}
	if arena.InUse() != 0 {
		t.Fatalf("arena blocks not returned on Reset: inUse=%d", arena.InUse())
	}

	// 6. Huge payload (1 MB > 512 KB, tests spillover to chunkbuffer)
	hugePayload := make([]byte, 1024*1024)
	for i := range hugePayload {
		hugePayload[i] = byte(i % 127)
	}
	n, err = buf.Write(hugePayload)
	if err != nil || n != len(hugePayload) {
		t.Fatalf("huge write failed: %v", err)
	}
	if buf.chunk == nil {
		t.Fatalf("expected fallback chunkbuffer to be active for 1MB payload")
	}
	if buf.Len() != len(hugePayload) {
		t.Fatalf("huge Len mismatch: %d", buf.Len())
	}
	if !bytes.Equal(buf.Bytes(), hugePayload) {
		t.Fatalf("huge Bytes content mismatch")
	}

	buf.Close()
	if arena.InUse() != 0 {
		t.Fatalf("arena blocks not returned after Close: inUse=%d", arena.InUse())
	}
}

func BenchmarkSlabArena_AllocFree(b *testing.B) {
	arena := NewSlabArena()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, id, _ := arena.AllocBlock()
		arena.FreeBlock(id)
	}
}

func BenchmarkArenaBuffer_Write64K(b *testing.B) {
	arena := NewSlabArena()
	buf := NewArenaBuffer(arena)
	defer buf.Close()
	payload := make([]byte, 64*1024)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		_, _ = buf.Write(payload)
	}
}
