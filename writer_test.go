package chunkbuffer

import (
	"bytes"
	"errors"
	"testing"
)

// TestWriteToWriterError covers the branch where the writer fails midway: the
// bytes already written must still be consumed, so the buffer stays coherent.
func TestWriteToWriterError(t *testing.T) {
	sentinel := errors.New("boom")
	b := NewBuffer(4)
	defer b.Close()
	b.Write([]byte("abcdefghij")) // three blocks: 4+4+2

	w := errAfter{n: 4, err: sentinel}
	n, err := b.WriteTo(&w)
	if !errors.Is(err, sentinel) {
		t.Fatalf("WriteTo err = %v, want %v", err, sentinel)
	}
	if n != 4 {
		t.Fatalf("WriteTo n = %d, want 4", n)
	}
	if b.Len() != 6 {
		t.Fatalf("Len() = %d after partial WriteTo, want 6", b.Len())
	}
	if got := b.String(); got != "efghij" {
		t.Fatalf("remaining data = %q, want %q", got, "efghij")
	}
	checkInvariants(t, b)

	// The buffer must remain fully usable.
	var out bytes.Buffer
	if _, err := b.WriteTo(&out); err != nil {
		t.Fatalf("second WriteTo: %v", err)
	}
	if out.String() != "efghij" {
		t.Fatalf("second WriteTo = %q, want %q", out.String(), "efghij")
	}
}

// errAfter writes n bytes successfully, then fails.
type errAfter struct {
	n   int
	err error
}

func (e *errAfter) Write(p []byte) (int, error) {
	if e.n <= 0 {
		return 0, e.err
	}
	if len(p) > e.n {
		p = p[:e.n]
	}
	e.n -= len(p)
	return len(p), nil
}

// TestReadFromOverReportedRead covers a misbehaving reader that claims to have
// written more than the destination had room for. Reading past the block would
// corrupt memory, so it must panic.
func TestReadFromOverReportedRead(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for a reader returning more than requested")
		}
	}()
	b := NewBuffer(4)
	defer b.Close()
	b.ReadFrom(overReader{})
}

type overReader struct{}

func (overReader) Read(p []byte) (int, error) { return len(p) + 10, nil }
