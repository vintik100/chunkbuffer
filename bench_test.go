package chunkbuffer

import (
	"bytes"
	"io"
	"runtime"
	"testing"
)

// The benchmarks below compare Buffer against bytes.Buffer on identical work.
// They are the source of the numbers in README.md: if a figure is quoted
// there, it comes from here.

var benchPayload = bytes.Repeat([]byte("1234567890"), 25<<10) // ~250 KiB
var benchStringPayload = string(benchPayload)

// BenchmarkWrite measures refilling a buffer after Reset, which is the case
// where bytes.Buffer is at its best (it keeps its capacity).
func BenchmarkWrite(b *testing.B) {
	b.Run("chunkbuffer", func(b *testing.B) {
		buf := NewBuffer()
		defer buf.Close()
		buf.Write(benchPayload)
		buf.Reset()
		b.SetBytes(int64(len(benchPayload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Write(benchPayload)
			buf.Reset()
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		var buf bytes.Buffer
		b.SetBytes(int64(len(benchPayload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Write(benchPayload)
			buf.Reset()
		}
	})
}

// BenchmarkWriteString compares chunkbuffer.WriteString against bytes.Buffer.WriteString.
func BenchmarkWriteString(b *testing.B) {
	b.Run("chunkbuffer", func(b *testing.B) {
		buf := NewBuffer()
		defer buf.Close()
		buf.WriteString(benchStringPayload)
		buf.Reset()
		b.SetBytes(int64(len(benchStringPayload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.WriteString(benchStringPayload)
			buf.Reset()
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		var buf bytes.Buffer
		b.SetBytes(int64(len(benchStringPayload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.WriteString(benchStringPayload)
			buf.Reset()
		}
	})
}

// BenchmarkRoundTrip measures write followed by a full read per iteration.
func BenchmarkRoundTrip(b *testing.B) {
	b.Run("chunkbuffer", func(b *testing.B) {
		buf := NewBuffer()
		defer buf.Close()
		sink := make([]byte, len(benchPayload))
		b.SetBytes(int64(len(benchPayload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Write(benchPayload)
			buf.Read(sink)
			buf.Reset()
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		var buf bytes.Buffer
		sink := make([]byte, len(benchPayload))
		b.SetBytes(int64(len(benchPayload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Write(benchPayload)
			buf.Read(sink)
			buf.Reset()
		}
	})
}

// BenchmarkWriteByte compares WriteByte against bytes.Buffer.WriteByte.
func BenchmarkWriteByte(b *testing.B) {
	b.Run("chunkbuffer", func(b *testing.B) {
		buf := NewBuffer()
		defer buf.Close()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.WriteByte('a')
			if buf.Len() >= len(benchPayload) {
				buf.Reset()
			}
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		var buf bytes.Buffer
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.WriteByte('a')
			if buf.Len() >= len(benchPayload) {
				buf.Reset()
			}
		}
	})
}

// BenchmarkReadByte compares ReadByte against bytes.Buffer.ReadByte.
func BenchmarkReadByte(b *testing.B) {
	b.Run("chunkbuffer", func(b *testing.B) {
		buf := NewBuffer()
		defer buf.Close()
		buf.Write(benchPayload)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := buf.ReadByte()
			if err != nil {
				buf.Reset()
				buf.Write(benchPayload)
			}
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		var buf bytes.Buffer
		buf.Write(benchPayload)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := buf.ReadByte()
			if err != nil {
				buf.Reset()
				buf.Write(benchPayload)
			}
		}
	})
}

// BenchmarkWriteTo measures streaming the buffer into an io.Writer.
func BenchmarkWriteTo(b *testing.B) {
	b.Run("chunkbuffer", func(b *testing.B) {
		buf := NewBuffer()
		defer buf.Close()
		b.SetBytes(int64(len(benchPayload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Write(benchPayload)
			buf.WriteTo(io.Discard)
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		var buf bytes.Buffer
		b.SetBytes(int64(len(benchPayload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Write(benchPayload)
			buf.WriteTo(io.Discard)
		}
	})
}

// BenchmarkReadFrom measures filling the buffer from an io.Reader.
func BenchmarkReadFrom(b *testing.B) {
	b.Run("chunkbuffer", func(b *testing.B) {
		buf := NewBuffer()
		defer buf.Close()
		b.SetBytes(int64(len(benchPayload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r := bytes.NewReader(benchPayload)
			buf.ReadFrom(r)
			buf.Reset()
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		var buf bytes.Buffer
		b.SetBytes(int64(len(benchPayload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r := bytes.NewReader(benchPayload)
			buf.ReadFrom(r)
			buf.Reset()
		}
	})
}

var growBlock = bytes.Repeat([]byte("x"), 32<<10)

// BenchmarkGrowFromEmpty measures the case chunking is meant for: growing a
// buffer from nothing, where bytes.Buffer has to keep doubling and copying.
func BenchmarkGrowFromEmpty(b *testing.B) {
	const total = 8 << 20
	const blocks = total / (32 << 10)

	b.Run("chunkbuffer", func(b *testing.B) {
		b.SetBytes(total)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			buf := NewBuffer()
			for j := 0; j < blocks; j++ {
				buf.Write(growBlock)
			}
			buf.Close()
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		b.SetBytes(total)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var buf bytes.Buffer
			for j := 0; j < blocks; j++ {
				buf.Write(growBlock)
			}
		}
	})
}

// BenchmarkGrowPreallocated compares growing from empty with and without Grow().
func BenchmarkGrowPreallocated(b *testing.B) {
	const total = 8 << 20
	const blocks = total / (32 << 10)

	b.Run("chunkbuffer/with-grow", func(b *testing.B) {
		b.SetBytes(total)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			buf := NewBuffer()
			buf.Grow(total)
			for j := 0; j < blocks; j++ {
				buf.Write(growBlock)
			}
			buf.Close()
		}
	})
	b.Run("chunkbuffer/no-grow", func(b *testing.B) {
		b.SetBytes(total)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			buf := NewBuffer()
			for j := 0; j < blocks; j++ {
				buf.Write(growBlock)
			}
			buf.Close()
		}
	})
	b.Run("stdlib/with-grow", func(b *testing.B) {
		b.SetBytes(total)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var buf bytes.Buffer
			buf.Grow(total)
			for j := 0; j < blocks; j++ {
				buf.Write(growBlock)
			}
		}
	})
}

// TestPeakAllocation compares transient heap use while growing to 64 MiB.
// It asserts the property the package exists for: the chunked buffer must not
// spike above a small multiple of the data it holds, while bytes.Buffer
// doubles and therefore overshoots substantially.
func TestPeakAllocation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping peak allocation test in -short mode")
	}
	const total = 64 << 20
	const blockSize = 32 << 10
	const blocks = total / blockSize

	measure := func(f func()) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		f()
		runtime.ReadMemStats(&after)
		if after.TotalAlloc < before.TotalAlloc {
			return 0
		}
		return after.TotalAlloc - before.TotalAlloc
	}

	chunkedAlloc := measure(func() {
		buf := NewBuffer()
		for j := 0; j < blocks; j++ {
			buf.Write(growBlock)
		}
		buf.Close()
	})

	stdAlloc := measure(func() {
		var buf bytes.Buffer
		for j := 0; j < blocks; j++ {
			buf.Write(growBlock)
		}
	})

	t.Logf("growing to %d MiB: chunkbuffer allocated %d MiB total, bytes.Buffer allocated %d MiB total",
		total>>20, chunkedAlloc>>20, stdAlloc>>20)

	if chunkedAlloc > 2*uint64(total) {
		t.Errorf("chunkbuffer allocated %d MiB for %d MiB of data; expected near 1x",
			chunkedAlloc>>20, total>>20)
	}
	if stdAlloc <= chunkedAlloc {
		t.Logf("note: bytes.Buffer did not overshoot on this run; the doubling " +
			"advantage of chunkbuffer is not observable here")
	}
}
