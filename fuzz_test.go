package chunkbuffer

import (
	"bytes"
	"io"
	"testing"
)

// FuzzBuffer drives random payloads through arbitrary block sizes and
// requires an exact round-trip. It targets the cursor arithmetic, which is
// where the original implementation was wrong.
func FuzzBuffer(f *testing.F) {
	f.Add([]byte("hello"), 7, 0)
	f.Add([]byte(""), 1, 0)
	f.Add([]byte("a"), 1, 0)
	f.Add(bytes.Repeat([]byte("x"), 100), 8, 3)
	f.Add(bytes.Repeat([]byte("y"), 64), 1, 63)
	f.Add(bytes.Repeat([]byte("z"), 33), 5, 17)

	f.Fuzz(func(t *testing.T, data []byte, chunkSize, consumed int) {
		if chunkSize < 1 {
			return
		}
		if len(data) > 1<<20 {
			return
		}
		b := NewBuffer(chunkSize)
		defer b.Close()

		if _, err := b.Write(data); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if b.Len() != len(data) {
			t.Fatalf("Len() = %d, want %d", b.Len(), len(data))
		}
		if got := b.Bytes(); !bytes.Equal(got, data) {
			t.Fatalf("Bytes() mismatch: len=%d want %d", len(got), len(data))
		}

		// Consume an arbitrary prefix, then check both read paths agree.
		if consumed < 0 {
			consumed = 0
		}
		if consumed > len(data) {
			consumed = len(data)
		}
		if consumed > 0 {
			head := make([]byte, consumed)
			if n, err := b.Read(head); n != consumed || err != nil {
				t.Fatalf("Read prefix = (%d, %v), want (%d, nil)", n, err, consumed)
			}
			if !bytes.Equal(head, data[:consumed]) {
				t.Fatalf("prefix mismatch")
			}
		}

		rest := data[consumed:]
		if got := b.Bytes(); !bytes.Equal(got, rest) {
			t.Fatalf("Bytes() after Read mismatch: len=%d want %d", len(got), len(rest))
		}

		var out bytes.Buffer
		n, err := b.WriteTo(&out)
		if err != nil {
			t.Fatalf("WriteTo: %v", err)
		}
		if n != int64(len(rest)) {
			t.Fatalf("WriteTo n = %d, want %d", n, len(rest))
		}
		if !bytes.Equal(out.Bytes(), rest) {
			t.Fatalf("WriteTo content mismatch")
		}
		if b.Len() != 0 {
			t.Fatalf("Len() = %d after WriteTo, want 0", b.Len())
		}
	})
}

// FuzzReadFrom requires ReadFrom to agree with io.ReadAll for arbitrary
// reader chunking, including pathological (0, nil) readers.
func FuzzReadFrom(f *testing.F) {
	f.Add([]byte("hello world"), 4, 0)
	f.Add([]byte("abc"), 1, 2)
	f.Add(bytes.Repeat([]byte("q"), 50), 3, 7)

	f.Fuzz(func(t *testing.T, data []byte, chunkSize, drip int) {
		if chunkSize < 1 || len(data) > 1<<20 {
			return
		}
		if drip < 0 {
			drip = 0
		}
		rd := &dripReader{src: data, max: drip, stallEvery: 0}

		b := NewBuffer(chunkSize)
		defer b.Close()

		n, err := b.ReadFrom(rd)
		if err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		if n != int64(len(data)) {
			t.Fatalf("ReadFrom n = %d, want %d", n, len(data))
		}
		if got := b.Bytes(); !bytes.Equal(got, data) {
			t.Fatalf("content mismatch: len=%d want %d", len(got), len(data))
		}

		// Drain via io.Copy and confirm it matches too.
		var out bytes.Buffer
		if _, err := io.Copy(&out, b); err != nil {
			t.Fatalf("io.Copy: %v", err)
		}
		if !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("io.Copy mismatch")
		}
	})
}
