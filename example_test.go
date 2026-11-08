package chunkbuffer_test

import (
	"bytes"
	"fmt"
	"io"

	"github.com/vintik100/chunkbuffer"
)

// Example shows the common write-then-read pattern.
func Example() {
	b := chunkbuffer.NewBuffer()
	defer b.Close()

	b.Write([]byte("hello, "))
	b.Write([]byte("world"))

	fmt.Println(b.Len())
	fmt.Println(b.String())
	// Output:
	// 12
	// hello, world
}

// ExampleBuffer_WriteTo shows draining a buffer into an io.Writer.
func ExampleBuffer_WriteTo() {
	b := chunkbuffer.NewBuffer(4)
	defer b.Close()

	b.Write([]byte("chunked"))

	var out bytes.Buffer
	n, _ := b.WriteTo(&out)
	fmt.Println(n, out.String(), b.Len())
	// Output:
	// 7 chunked 0
}

// ExampleNewBuffer_size shows that the block size follows the buffer.
func ExampleNewBuffer_size() {
	small := chunkbuffer.NewBuffer(4)
	large := chunkbuffer.NewBuffer(4096)
	defer small.Close()
	defer large.Close()

	payload := bytes.Repeat([]byte("x"), 100)
	small.Write(payload)
	large.Write(payload)

	fmt.Println(small.Len(), large.Len())
	fmt.Println(small.String() == large.String())
	// Output:
	// 100 100
	// true
}

// ExampleBuffer_Read shows io.Reader behaviour: a short read returns the bytes
// with a nil error, and io.EOF only appears once the buffer is empty.
func ExampleBuffer_Read() {
	b := chunkbuffer.NewBuffer()
	defer b.Close()
	b.Write([]byte("abc"))

	p := make([]byte, 8)
	n, err := b.Read(p)
	fmt.Printf("%d %v %q\n", n, err, p[:n])

	n, err = b.Read(p)
	fmt.Printf("%d %v\n", n, err)
	// Output:
	// 3 <nil> "abc"
	// 0 EOF
}

// ExampleBuffer_Reset shows that Reset keeps capacity for reuse.
func ExampleBuffer_Reset() {
	b := chunkbuffer.NewBuffer()
	defer b.Close()

	b.Write(bytes.Repeat([]byte("x"), 10_000))
	capBefore := b.Cap()

	b.Reset()
	b.Write([]byte("again"))

	fmt.Println(b.Len(), b.String(), capBefore >= b.Cap())
	// Output:
	// 5 again true
}

// ExampleBuffer_SetMaxSize shows the limit that protects against unbounded
// readers.
func ExampleBuffer_SetMaxSize() {
	b := chunkbuffer.NewBuffer()
	defer b.Close()
	b.SetMaxSize(1 << 10)

	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("stopped:", r)
			}
		}()
		// A reader larger than the limit bumps into ErrTooLarge.
		io.Copy(b, bytes.NewReader(make([]byte, 4096)))
	}()
	// Output:
	// stopped: chunkbuffer: too large
}

// ExampleBuffer_WriteString shows appending strings without slice conversion allocations.
func ExampleBuffer_WriteString() {
	b := chunkbuffer.NewBuffer()
	defer b.Close()

	b.WriteString("fast ")
	b.WriteString("string ")
	b.WriteString("writing")

	fmt.Println(b.String())
	// Output:
	// fast string writing
}

// ExampleBuffer_WriteByte shows byte-at-a-time writing and reading.
func ExampleBuffer_WriteByte() {
	b := chunkbuffer.NewBuffer()
	defer b.Close()

	b.WriteByte('A')
	b.WriteByte('B')
	b.WriteByte('C')

	ch, _ := b.ReadByte()
	fmt.Printf("%c %s\n", ch, b.String())
	// Output:
	// A BC
}

// ExampleBuffer_Grow shows preallocating capacity to avoid allocations on subsequent writes.
func ExampleBuffer_Grow() {
	b := chunkbuffer.NewBuffer()
	defer b.Close()

	b.Grow(1000)
	fmt.Println(b.Cap() >= 1000)
	// Output:
	// true
}
