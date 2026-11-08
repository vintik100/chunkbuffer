package chunkbuffer

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"strings"
	"sync"
	"testing"
)

// TestWriteReadRoundTrip checks byte-exact round-trips across block
// boundaries, including the 1-byte and chunkSize±1 edges.
func TestWriteReadRoundTrip(t *testing.T) {
	const cs = 16
	sizes := []int{0, 1, cs - 1, cs, cs + 1, 2*cs - 1, 2 * cs, 2*cs + 1, 3*cs + 7, 1 << 10, 1 << 16}

	for _, size := range sizes {
		t.Run(strings.ReplaceAll(strings.TrimSpace(itoa(size)), " ", "_"), func(t *testing.T) {
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i%251 + 1)
			}

			b := NewBuffer(cs)
			defer b.Close()

			n, err := b.Write(payload)
			if err != nil || n != size {
				t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, size)
			}
			if got := b.Len(); got != size {
				t.Fatalf("Len() = %d, want %d", got, size)
			}

			// Bytes must be a faithful copy.
			if got := b.Bytes(); !bytes.Equal(got, payload) {
				t.Fatalf("Bytes() mismatch: len=%d want %d", len(got), size)
			}

			out, err := io.ReadAll(b)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if !bytes.Equal(out, payload) {
				t.Fatalf("round-trip mismatch: len=%d want %d", len(out), size)
			}
			if b.Len() != 0 {
				t.Fatalf("Len() = %d after full read, want 0", b.Len())
			}
		})
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[p:])
}

// TestDifferential replays random interleavings of Write and Read against
// bytes.Buffer and requires identical results at every step. This is the
// test that proves the cursor arithmetic is right.
func TestDifferential(t *testing.T) {
	for _, cs := range []int{1, 3, 16, DefaultChunkSize} {
		t.Run(itoa(cs), func(t *testing.T) {
			rng := rand.New(rand.NewSource(1))
			for trial := 0; trial < 40; trial++ {
				b := NewBuffer(cs)
				var ref bytes.Buffer

				for step := 0; step < 50; step++ {
					if rng.Intn(3) < 2 {
						p := make([]byte, rng.Intn(3*cs)+1)
						rng.Read(p)
						n1, e1 := b.Write(p)
						n2, e2 := ref.Write(p)
						if n1 != n2 || e1 != e2 {
							t.Fatalf("trial %d step %d: Write = (%d,%v) want (%d,%v)",
								trial, step, n1, e1, n2, e2)
						}
					} else {
						size := rng.Intn(3*cs) + 1
						p1 := make([]byte, size)
						p2 := make([]byte, size)
						n1, e1 := b.Read(p1)
						n2, e2 := ref.Read(p2)

						if n1 != n2 {
							t.Fatalf("trial %d step %d: Read n = %d want %d (Len before=%d)",
								trial, step, n1, n2, ref.Len())
						}
						if (e1 == nil) != (e2 == nil) {
							t.Fatalf("trial %d step %d: Read err = %v want %v", trial, step, e1, e2)
						}
						if !bytes.Equal(p1[:n1], p2[:n2]) {
							t.Fatalf("trial %d step %d: Read content mismatch", trial, step)
						}
					}
					if b.Len() != ref.Len() {
						t.Fatalf("trial %d step %d: Len = %d want %d", trial, step, b.Len(), ref.Len())
					}
				}
			}
		})
	}
}

// TestReadContract pins the io.Reader convention: a non-zero n never comes
// with io.EOF. The original returned (n>0, io.EOF), which broke the common
// "read then check err" loop.
func TestReadContract(t *testing.T) {
	b := NewBuffer(4)
	defer b.Close()
	b.Write([]byte("abcdef"))

	p := make([]byte, 10)
	n, err := b.Read(p)
	if n != 6 {
		t.Fatalf("Read n = %d, want 6", n)
	}
	if err != nil {
		t.Fatalf("Read returned n=%d with err=%v; want nil error when n > 0", n, err)
	}

	// Now empty.
	n, err = b.Read(p)
	if n != 0 || err != io.EOF {
		t.Fatalf("Read on drained buffer = (%d, %v), want (0, io.EOF)", n, err)
	}

	// Zero-length read.
	n, err = b.Read(nil)
	if n != 0 || err != nil {
		t.Fatalf("Read(nil) = (%d, %v), want (0, nil)", n, err)
	}
}

// TestZeroValue requires the zero value to be usable, like bytes.Buffer.
func TestZeroValue(t *testing.T) {
	var b Buffer
	defer b.Close()

	if got := b.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}
	if got := b.Bytes(); len(got) != 0 {
		t.Fatalf("Bytes() = %q, want empty", got)
	}
	if got := b.String(); got != "" {
		t.Fatalf("String() = %q, want empty", got)
	}
	if got := b.Cap(); got != 0 {
		t.Fatalf("Cap() = %d, want 0", got)
	}

	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := b.String(); got != "hello" {
		t.Fatalf("String() = %q, want %q", got, "hello")
	}

	// Read and reset on the zero value.
	io.ReadAll(&b)
	b.Reset()
	if _, err := b.Write([]byte("again")); err != nil {
		t.Fatalf("Write after Reset: %v", err)
	}
	if got := b.String(); got != "again" {
		t.Fatalf("String() = %q, want %q", got, "again")
	}
	b.Close() // idempotent
	b.Close()
}

// TestWriteToPartial is the regression test for the corruption bug: WriteTo
// used to emit whole blocks regardless of Len(), producing Len() < 0 and
// garbage bytes.
func TestWriteToPartial(t *testing.T) {
	for _, size := range []int{1, 2, 3, 7, 8, 9, 15, 16, 17, 100} {
		for _, consumed := range []int{0, 1, 2, 5} {
			if consumed > size {
				continue
			}
			t.Run(itoa(size)+"_after_"+itoa(consumed), func(t *testing.T) {
				payload := make([]byte, size)
				for i := range payload {
					payload[i] = byte('a' + i%26)
				}
				b := NewBuffer(8)
				defer b.Close()
				b.Write(payload)

				if consumed > 0 {
					head := make([]byte, consumed)
					if n, _ := b.Read(head); n != consumed {
						t.Fatalf("setup read = %d, want %d", n, consumed)
					}
					if !bytes.Equal(head, payload[:consumed]) {
						t.Fatalf("setup read content mismatch")
					}
				}

				want := payload[consumed:]
				var out bytes.Buffer
				n, err := b.WriteTo(&out)
				if err != nil {
					t.Fatalf("WriteTo: %v", err)
				}
				if n != int64(len(want)) {
					t.Fatalf("WriteTo n = %d, want %d", n, len(want))
				}
				if out.Len() != len(want) {
					t.Fatalf("wrote %d bytes, want %d (over-read!)", out.Len(), len(want))
				}
				if !bytes.Equal(out.Bytes(), want) {
					t.Fatalf("WriteTo content mismatch")
				}
				if b.Len() != 0 {
					t.Fatalf("Len() = %d after WriteTo, want 0", b.Len())
				}
				// The buffer must remain usable afterwards.
				if _, err := b.Write([]byte("x")); err != nil {
					t.Fatalf("Write after WriteTo: %v", err)
				}
				if b.String() != "x" {
					t.Fatalf("String() = %q, want %q", b.String(), "x")
				}
			})
		}
	}
}

// TestNoMemoryDisclosure is the regression test for cross-buffer leakage:
// blocks recycled from another buffer must not surface stale bytes.
func TestNoMemoryDisclosure(t *testing.T) {
	const secret = "AWS_SECRET_ACCESS_KEY=abcd1234EFGH5678ijkl"

	victim := NewBuffer(8)
	victim.Write([]byte(secret))
	victim.Write([]byte(strings.Repeat("filler", 4)))
	victim.Close()

	for i := 0; i < 8; i++ {
		b := NewBuffer(8)
		b.Write([]byte("hi"))
		var out bytes.Buffer
		if _, err := b.WriteTo(&out); err != nil {
			t.Fatalf("WriteTo: %v", err)
		}
		if strings.Contains(out.String(), secret) {
			t.Fatalf("WriteTo leaked recycled memory: %q", out.String())
		}
		if out.String() != "hi" {
			t.Fatalf("WriteTo wrote %q, want %q", out.String(), "hi")
		}
		if got := b.String(); strings.Contains(got, secret) {
			t.Fatalf("Bytes/String leaked recycled memory: %q", got)
		}
		b.Close()
	}
}

// TestStreamingMemory is the regression test for the unbounded growth: a
// long-lived buffer used write-then-read must stay at constant memory.
func TestStreamingMemory(t *testing.T) {
	b := NewBuffer(4096)
	defer b.Close()

	payload := bytes.Repeat([]byte("x"), 4096)
	sink := make([]byte, 4096)

	for i := 0; i < 10000; i++ {
		if _, err := b.Write(payload); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if _, err := b.Read(sink); err != nil {
			t.Fatalf("Read: %v", err)
		}
	}

	// Only live blocks matter: consumed entries before head are nil slots that
	// compaction reclaims. Live blocks and live capacity must stay constant.
	live := len(b.chunks) - b.head
	if live > 2 {
		t.Fatalf("retained %d live blocks after 10000 cycles, want <= 2", live)
	}
	if got := b.Cap(); got > 2*4096 {
		t.Fatalf("Cap() = %d after 10000 cycles, want <= %d", got, 2*4096)
	}
	// The backing slice must be bounded too, otherwise slot bookkeeping leaks.
	if len(b.chunks) > compactThreshold+2 {
		t.Fatalf("block slice grew to %d entries; compaction is not running", len(b.chunks))
	}
	if b.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", b.Len())
	}
}

// TestDoubleClose is the regression test for pool corruption: closing twice
// must not put the same block back twice, which let two buffers share memory.
func TestDoubleClose(t *testing.T) {
	b := NewBuffer(8)
	b.Write([]byte("hello world"))
	b.Close()
	b.Close()
	b.Close()

	// After Close the buffer must still work.
	if _, err := b.Write([]byte("reuse")); err != nil {
		t.Fatalf("Write after Close: %v", err)
	}
	if got := b.String(); got != "reuse" {
		t.Fatalf("String() = %q, want %q", got, "reuse")
	}
	b.Close()

	// Two live buffers must never share a block.
	x := NewBuffer(8)
	y := NewBuffer(8)
	defer x.Close()
	defer y.Close()
	x.Write([]byte("aaaaaaaa"))
	y.Write([]byte("bbbbbbbb"))

	gotX := x.String()
	gotY := y.String()
	if gotX != "aaaaaaaa" || gotY != "bbbbbbbb" {
		t.Fatalf("buffers interfere: x=%q y=%q", gotX, gotY)
	}
}

// TestChunkSizePerInstance checks that block size is per-buffer and that no
// package-level mutable state is needed.
func TestChunkSizePerInstance(t *testing.T) {
	small := NewBuffer(4)
	large := NewBuffer(1024)
	defer small.Close()
	defer large.Close()

	if got := small.size(); got != 4 {
		t.Fatalf("small.size() = %d, want 4", got)
	}
	if got := large.size(); got != 1024 {
		t.Fatalf("large.size() = %d, want 1024", got)
	}

	small.Write([]byte("abcdefgh"))
	large.Write([]byte("abcdefgh"))
	if small.String() != "abcdefgh" || large.String() != "abcdefgh" {
		t.Fatalf("independent buffers disagreed: %q %q", small.String(), large.String())
	}

	// Constructor fallbacks.
	if got := NewBuffer().size(); got != DefaultChunkSize {
		t.Fatalf("NewBuffer().size() = %d, want %d", got, DefaultChunkSize)
	}
	if got := NewBuffer(0).size(); got != DefaultChunkSize {
		t.Fatalf("NewBuffer(0).size() = %d, want %d", got, DefaultChunkSize)
	}
	if got := NewBuffer(-5).size(); got != DefaultChunkSize {
		t.Fatalf("NewBuffer(-5).size() = %d, want %d", got, DefaultChunkSize)
	}
}

// TestConcurrentDistinctBuffers exercises the shared pool from many
// goroutines, each with its own buffer. Run with -race.
func TestConcurrentDistinctBuffers(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				b := NewBuffer(16)
				payload := bytes.Repeat([]byte{byte(g)}, 100)
				if _, err := b.Write(payload); err != nil {
					t.Errorf("Write: %v", err)
					b.Close()
					return
				}
				got := b.Bytes()
				if !bytes.Equal(got, payload) {
					t.Errorf("goroutine %d: data mismatch", g)
				}
				b.Close()
			}
		}(g)
	}
	wg.Wait()
}

// dripReader returns small chunks and occasional (0, nil), like a net.Conn.
// max caps the bytes per Read (0 means no cap); stallEvery makes every Nth
// Read report (0, nil).
type dripReader struct {
	src        []byte
	pos        int
	i          int
	max        int
	stallEvery int
}

func (d *dripReader) Read(p []byte) (int, error) {
	d.i++
	if d.stallEvery > 0 && d.i%d.stallEvery == 0 {
		return 0, nil
	}
	if d.pos >= len(d.src) {
		return 0, io.EOF
	}
	n := len(p)
	if d.max > 0 && n > d.max {
		n = d.max
	} else if d.max == 0 && n > 100 {
		n = 100
	}
	if d.pos+n > len(d.src) {
		n = len(d.src) - d.pos
	}
	copy(p, d.src[d.pos:d.pos+n])
	d.pos += n
	return n, nil
}

// TestReadFromPartialReader covers readers that return fewer bytes than
// requested, which is the normal case for sockets.
func TestReadFromPartialReader(t *testing.T) {
	src := bytes.Repeat([]byte("abcdefgh"), 5000)
	b := NewBuffer(16)
	defer b.Close()

	n, err := b.ReadFrom(&dripReader{src: src})
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if n != int64(len(src)) {
		t.Fatalf("ReadFrom n = %d, want %d", n, len(src))
	}
	if !bytes.Equal(b.Bytes(), src) {
		t.Fatalf("ReadFrom content mismatch")
	}
}

// TestReadFromIOCopy checks the full io.Copy pipeline in both directions.
func TestReadFromIOCopy(t *testing.T) {
	src := bytes.Repeat([]byte("0123456789"), 20000)

	b := NewBuffer()
	defer b.Close()
	if _, err := io.Copy(b, bytes.NewReader(src)); err != nil {
		t.Fatalf("io.Copy into buffer: %v", err)
	}
	if b.Len() != len(src) {
		t.Fatalf("Len() = %d, want %d", b.Len(), len(src))
	}

	var out bytes.Buffer
	n, err := io.Copy(&out, b)
	if err != nil {
		t.Fatalf("io.Copy out of buffer: %v", err)
	}
	if n != int64(len(src)) || !bytes.Equal(out.Bytes(), src) {
		t.Fatalf("io.Copy round-trip mismatch: n=%d", n)
	}
	if b.Len() != 0 {
		t.Fatalf("Len() = %d after drain, want 0", b.Len())
	}
}

// TestNegativeReadPanics pins the documented panic.
func TestNegativeReadPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != ErrNegativeRead {
			t.Fatalf("expected panic with ErrNegativeRead, got %v", r)
		}
	}()
	b := NewBuffer(8)
	defer b.Close()
	b.ReadFrom(negReader{})
}

type negReader struct{}

func (negReader) Read(p []byte) (int, error) { return -1, nil }

// TestErrTooLarge checks the limit that the old docstring promised but never
// enforced.
func TestErrTooLarge(t *testing.T) {
	t.Run("Write", func(t *testing.T) {
		b := NewBuffer(8)
		defer b.Close()
		b.SetMaxSize(10)
		b.Write([]byte("12345678"))

		defer func() {
			if r := recover(); r != ErrTooLarge {
				t.Fatalf("expected ErrTooLarge, got %v", r)
			}
		}()
		b.Write([]byte("abc"))
	})

	t.Run("ReadFrom", func(t *testing.T) {
		b := NewBuffer(8)
		defer b.Close()
		b.SetMaxSize(10)

		defer func() {
			if r := recover(); r != ErrTooLarge {
				t.Fatalf("expected ErrTooLarge, got %v", r)
			}
		}()
		b.ReadFrom(&dripReader{src: bytes.Repeat([]byte("x"), 100)})
	})

	t.Run("within limit", func(t *testing.T) {
		b := NewBuffer(8)
		defer b.Close()
		b.SetMaxSize(10)
		if _, err := b.Write([]byte("1234567890")); err != nil {
			t.Fatalf("Write within limit: %v", err)
		}
		if got := b.Len(); got != 10 {
			t.Fatalf("Len() = %d, want 10", got)
		}
	})
}

// TestBytesIsCopy documents the deliberate divergence from bytes.Buffer.
func TestBytesIsCopy(t *testing.T) {
	b := NewBuffer(8)
	defer b.Close()
	b.Write([]byte("hello"))

	got := b.Bytes()
	got[0] = 'X'
	if b.String() != "hello" {
		t.Fatalf("mutating Bytes() result changed the buffer: %q", b.String())
	}
}

// TestShortWriter covers a writer that reports a short write.
func TestShortWriter(t *testing.T) {
	b := NewBuffer(8)
	defer b.Close()
	b.Write([]byte("hello world"))

	_, err := b.WriteTo(shortWriter{})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("WriteTo err = %v, want io.ErrShortWrite", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

// TestWriteToError covers a writer that fails outright.
func TestWriteToError(t *testing.T) {
	sentinel := errors.New("boom")
	b := NewBuffer(8)
	defer b.Close()
	b.Write([]byte("hello world"))

	_, err := b.WriteTo(errWriter{sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WriteTo err = %v, want %v", err, sentinel)
	}
}

type errWriter struct{ err error }

func (e errWriter) Write(p []byte) (int, error) { return 0, e.err }

// TestResetReusesCapacity guards the streaming use case at the Reset level.
func TestResetReusesCapacity(t *testing.T) {
	b := NewBuffer(1024)
	defer b.Close()
	b.Write(bytes.Repeat([]byte("x"), 4096))
	before := b.Cap()

	b.Reset()
	if b.Len() != 0 || len(b.Bytes()) != 0 {
		t.Fatalf("Reset left data behind: Len=%d", b.Len())
	}
	if after := b.Cap(); after > before || after == 0 {
		t.Fatalf("Reset did not reuse capacity: before=%d after=%d", before, after)
	}

	b.Write(bytes.Repeat([]byte("y"), 4096))
	if b.Len() != 4096 {
		t.Fatalf("Len() = %d after refill, want 4096", b.Len())
	}

	// Refill using WriteString from free list
	b.Reset()
	b.WriteString(string(bytes.Repeat([]byte("z"), 4096)))
	if b.Len() != 4096 {
		t.Fatalf("Len() = %d after WriteString refill, want 4096", b.Len())
	}

	// Refill using ReadFrom from free list
	b.Reset()
	if n, err := b.ReadFrom(bytes.NewReader(bytes.Repeat([]byte("w"), 4096))); err != nil || n != 4096 {
		t.Fatalf("ReadFrom refill failed: n=%d, err=%v", n, err)
	}
}

// TestWriteString covers io.StringWriter implementation and multi-block writing.
func TestWriteString(t *testing.T) {
	b := NewBuffer(8)
	defer b.Close()

	if n, err := b.WriteString(""); err != nil || n != 0 {
		t.Fatalf("WriteString empty = (%d, %v), want (0, nil)", n, err)
	}

	n, err := b.WriteString("hello, ")
	if err != nil || n != 7 {
		t.Fatalf("WriteString = (%d, %v), want (7, nil)", n, err)
	}
	const suffix = "world! this is a longer string that spans several blocks."
	n, err = b.WriteString(suffix)
	if err != nil || n != len(suffix) {
		t.Fatalf("WriteString = (%d, %v), want (%d, nil)", n, err, len(suffix))
	}
	expected := "hello, " + suffix
	if b.String() != expected {
		t.Fatalf("String() = %q, want %q", b.String(), expected)
	}

	// SetMaxSize enforcement
	b.Reset()
	b.SetMaxSize(10)
	b.WriteString("12345")
	defer func() {
		if r := recover(); r != ErrTooLarge {
			t.Fatalf("expected ErrTooLarge panic, got %v", r)
		}
	}()
	b.WriteString("678901") // exceeds 10
}

// TestWriteByteAndReadByte covers io.ByteWriter and io.ByteReader.
func TestWriteByteAndReadByte(t *testing.T) {
	b := NewBuffer(4)
	defer b.Close()

	// Empty read
	if _, err := b.ReadByte(); err != io.EOF {
		t.Fatalf("ReadByte on empty = %v, want io.EOF", err)
	}

	data := "abcdefghijklmno"
	for i := 0; i < len(data); i++ {
		if err := b.WriteByte(data[i]); err != nil {
			t.Fatalf("WriteByte: %v", err)
		}
	}
	if b.Len() != len(data) {
		t.Fatalf("Len() = %d, want %d", b.Len(), len(data))
	}

	for i := 0; i < len(data); i++ {
		ch, err := b.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte[%d]: %v", i, err)
		}
		if ch != data[i] {
			t.Fatalf("ReadByte[%d] = %c, want %c", i, ch, data[i])
		}
	}

	if _, err := b.ReadByte(); err != io.EOF {
		t.Fatalf("ReadByte after drain = %v, want io.EOF", err)
	}

	// Limit enforcement
	b.Reset()
	b.SetMaxSize(2)
	b.WriteByte('a')
	b.WriteByte('b')
	defer func() {
		if r := recover(); r != ErrTooLarge {
			t.Fatalf("expected ErrTooLarge on WriteByte, got %v", r)
		}
	}()
	b.WriteByte('c')
}

// TestGrow guarantees capacity and verifies zero-allocation refilling.
func TestGrow(t *testing.T) {
	b := NewBuffer()
	defer b.Close()

	b.Grow(20000)
	if capLeft := b.Cap() - b.Len(); capLeft < 20000 {
		t.Fatalf("Cap() - Len() = %d, want >= 20000", capLeft)
	}

	// Calling Grow when room already suffices is a no-op
	capBefore := b.Cap()
	b.Grow(10)
	if b.Cap() != capBefore {
		t.Fatalf("Cap() changed on redundant Grow: %d != %d", b.Cap(), capBefore)
	}

	// Writing within grown capacity must not allocate.
	payload := bytes.Repeat([]byte("a"), 20000)
	n, err := b.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("Write = (%d, %v)", n, err)
	}
	if b.Len() != len(payload) {
		t.Fatalf("Len() = %d, want %d", b.Len(), len(payload))
	}

	// Test Grow panics
	t.Run("negative", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("expected panic on negative Grow")
			}
		}()
		b.Grow(-1)
	})

	t.Run("maxSize", func(t *testing.T) {
		b2 := NewBuffer()
		defer b2.Close()
		b2.SetMaxSize(100)
		defer func() {
			if r := recover(); r != ErrTooLarge {
				t.Fatalf("expected ErrTooLarge on Grow exceeding maxSize, got %v", r)
			}
		}()
		b2.Grow(200)
	})
}

// TestPoolHygiene verifies that blocks of a custom size are recycled through
// their own size class and never surface in default-sized buffers.
func TestPoolHygiene(t *testing.T) {
	// Allocate a buffer with oversized blocks and close it.
	custom := NewBuffer(64 * 1024)
	custom.Write(bytes.Repeat([]byte("x"), 128*1024))
	custom.Close()

	// Default buffer chunks pulled from pool must remain at DefaultChunkSize.
	def := NewBuffer()
	defer def.Close()
	def.Write(bytes.Repeat([]byte("y"), 32*1024))
	for i := def.head; i < len(def.chunks); i++ {
		if c := def.chunks[i]; c != nil && cap(c.B) != DefaultChunkSize {
			t.Fatalf("block %d in default buffer has cap %d, want %d", i, cap(c.B), DefaultChunkSize)
		}
	}
}

// TestDrainImmediateReset checks that completely draining a buffer resets head and chunks to 0.
func TestDrainImmediateReset(t *testing.T) {
	b := NewBuffer(16)
	defer b.Close()

	b.Write([]byte("0123456789abcdef")) // exactly 1 block
	sink := make([]byte, 16)
	b.Read(sink)

	if b.head != 0 {
		t.Fatalf("head = %d after full drain, want 0", b.head)
	}
	if len(b.chunks) != 0 {
		t.Fatalf("len(chunks) = %d after full drain, want 0", len(b.chunks))
	}
}

// errAfterData serves its data first, then fails with a non-EOF error, like a
// connection that dies mid-stream.
type errAfterData struct {
	data []byte
	pos  int
	err  error
}

func (r *errAfterData) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// TestReadFromNonEOFError covers ReadFrom passing through a reader error that
// is not io.EOF. The bytes read before the error must stay in the buffer.
func TestReadFromNonEOFError(t *testing.T) {
	sentinel := errors.New("connection reset")
	b := NewBuffer(8)
	defer b.Close()

	n, err := b.ReadFrom(&errAfterData{data: []byte("hello"), err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("ReadFrom err = %v, want %v", err, sentinel)
	}
	if n != 5 {
		t.Fatalf("ReadFrom n = %d, want 5", n)
	}
	if b.Len() != 5 || b.String() != "hello" {
		t.Fatalf("buffer = %q (Len %d), want %q", b.String(), b.Len(), "hello")
	}
	checkInvariants(t, b)
}

// TestGrowWithPartialTail covers Grow when the last live block is partially
// filled: the room already available must count toward the guarantee.
func TestGrowWithPartialTail(t *testing.T) {
	b := NewBuffer(8)
	defer b.Close()

	b.Write([]byte("abc")) // tail block: 3 of 8 bytes used
	b.Grow(20)

	if room := b.Cap() - b.Len(); room < 20 {
		t.Fatalf("Cap()-Len() = %d after Grow(20), want >= 20", room)
	}
	checkInvariants(t, b)

	payload := bytes.Repeat([]byte("z"), 20)
	if _, err := b.Write(payload); err != nil {
		t.Fatalf("Write after Grow: %v", err)
	}
	if got, want := b.String(), "abc"+string(payload); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	checkInvariants(t, b)
}

// TestGrowLargeChunkSize covers Grow reallocating pooled blocks that are
// smaller than the buffer's custom block size.
func TestGrowLargeChunkSize(t *testing.T) {
	const size = 64 << 10
	b := NewBuffer(size)
	defer b.Close()

	b.Grow(2 * size)
	for i, c := range b.free {
		if cap(c.B) < size {
			t.Fatalf("free block %d has cap %d, want >= %d", i, cap(c.B), size)
		}
	}

	payload := bytes.Repeat([]byte("w"), 2*size)
	if _, err := b.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if b.Len() != len(payload) {
		t.Fatalf("Len() = %d, want %d", b.Len(), len(payload))
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

// TestCustomSizeChunkPooling verifies that a buffer whose chunk size is larger
// than DefaultChunkSize recycles blocks through its own pool class: a
// drain-and-refill cycle must not allocate per block. Without size-class
// pooling such buffers lose all recycling and reallocate every block, which
// measured 4-5x slower round-trips at 32-256 KiB chunk sizes.
func TestCustomSizeChunkPooling(t *testing.T) {
	for _, size := range []int{32 << 10, 64 << 10, 256 << 10} {
		t.Run(itoa(size), func(t *testing.T) {
			b := NewBuffer(size)
			defer b.Close()

			payload := bytes.Repeat([]byte("x"), 2*size)
			sink := make([]byte, len(payload))

			// Warm the class pool so the measurement sees steady state.
			b.Write(payload)
			b.Read(sink)

			allocs := testing.AllocsPerRun(50, func() {
				b.Write(payload)
				b.Read(sink)
			})
			if allocs > 2 {
				t.Fatalf("drain/refill cycle allocated %.1f times per run; blocks are not being recycled", allocs)
			}
			checkInvariants(t, b)
		})
	}
}

func TestVisitChunksAndChunks(t *testing.T) {
	b := AcquireBuffer(16)
	defer ReleaseBuffer(b)

	if b.LiveChunkCount() != 0 {
		t.Fatalf("expected LiveChunkCount=0 for empty buffer, got %d", b.LiveChunkCount())
	}
	if b.Chunks() != nil {
		t.Fatalf("expected Chunks()=nil for empty buffer")
	}

	payload := []byte("The quick brown fox jumps over the lazy dog. 0123456789 ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	_, err := b.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if b.LiveChunkCount() == 0 {
		t.Fatalf("expected LiveChunkCount > 0, got 0")
	}

	chunks := b.Chunks()
	if len(chunks) != b.LiveChunkCount() {
		t.Fatalf("chunks count %d != LiveChunkCount %d", len(chunks), b.LiveChunkCount())
	}

	var assembled []byte
	b.VisitChunks(func(chunk []byte) bool {
		assembled = append(assembled, chunk...)
		return true
	})
	if !bytes.Equal(assembled, payload) {
		t.Fatalf("assembled chunks mismatch: got %q, want %q", assembled, payload)
	}

	// Early abort in VisitChunks
	visitCount := 0
	b.VisitChunks(func(chunk []byte) bool {
		visitCount++
		return false
	})
	if visitCount != 1 {
		t.Fatalf("expected VisitChunks early termination after 1 chunk, got %d", visitCount)
	}

	// CopyTo
	dst := make([]byte, len(payload))
	n := b.CopyTo(dst)
	if n != len(payload) || !bytes.Equal(dst, payload) {
		t.Fatalf("CopyTo mismatch: copied %d bytes, got %q", n, dst)
	}

	// Partial CopyTo
	shortDst := make([]byte, 10)
	nShort := b.CopyTo(shortDst)
	if nShort != 10 || !bytes.Equal(shortDst, payload[:10]) {
		t.Fatalf("partial CopyTo mismatch: copied %d bytes, got %q", nShort, shortDst)
	}

	// Ensure read cursor is preserved after VisitChunks & CopyTo
	if b.Len() != len(payload) {
		t.Fatalf("expected Len() unchanged (%d), got %d", len(payload), b.Len())
	}
}

func TestAcquireReleaseBuffer(t *testing.T) {
	b := AcquireBuffer()
	if b == nil {
		t.Fatal("AcquireBuffer returned nil")
	}
	b.WriteString("test payload string")
	ReleaseBuffer(b)

	b2 := AcquireBuffer(32)
	if b2 == nil {
		t.Fatal("AcquireBuffer(32) returned nil")
	}
	if b2.Len() != 0 {
		t.Fatalf("expected empty buffer from pool, got len=%d", b2.Len())
	}
	b2.WriteString("second test")
	ReleaseBuffer(b2)
	ReleaseBuffer(nil) // should be no-op safe
}
