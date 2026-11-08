# chunkbuffer

A byte buffer for Go that stores its contents in fixed-size blocks instead of
one contiguous slice.

```go
b := chunkbuffer.NewBuffer()
defer b.Close()

b.Write([]byte("hello, "))
b.Write([]byte("world"))

fmt.Println(b.String()) // hello, world
```

It implements `io.Reader`, `io.Writer`, `io.ByteReader`, `io.ByteWriter`,
`io.StringWriter`, `io.ReaderFrom` and `io.WriterTo`, so it drops into anything
that expects a buffer.

---

## Installation

```bash
go get github.com/vintik100/chunkbuffer
```

---

## When to use it

**Use `chunkbuffer` when you expect large payloads** and want to avoid the
growth spike of a single contiguous allocation.

`bytes.Buffer` grows by doubling: it allocates a new array twice the size and
copies. Growing to 64 MiB therefore allocates ~127 MiB in total. `chunkbuffer`
appends blocks and never copies, so it allocates ~64 MiB — measured in
[`TestPeakAllocation`](bench_test.go):

```
growing to 64 MiB: chunkbuffer allocated 64 MiB total, bytes.Buffer allocated 127 MiB total
```

It also never needs one huge contiguous block, which matters in fragmented
heaps, behind hard memory limits, or on 32-bit platforms.

## When *not* to use it

**Most of the time.** For small and medium payloads, `bytes.Buffer` from the
standard library is faster, has a bigger API surface, and is battle-tested.
This package ranges from roughly on par to ~15% slower per byte depending on
the read/write mix, because each block costs an indirection that a single
contiguous slice does not:

| Benchmark (250 KiB payload) | chunkbuffer | `bytes.Buffer` |
|---|---|---|
| Write + Reset | 4264 ns/op | 4164 ns/op |
| Write + Read + Reset | 10010 ns/op | 8648 ns/op |
| Grow from empty to 8 MiB | **172900 ns/op** | 1889000 ns/op |
| Total allocation, grow to 64 MiB | **64 MiB** | 127 MiB |

Numbers are medians of `go test -bench . -benchmem -count 6` on an AMD Ryzen 5
5600X; run-to-run variance is a few percent. Reproduce them before trusting
them on your hardware — see [`bench_test.go`](bench_test.go), which is the
source of this table.

The last two rows are the reason this package exists. The first two are the
price you pay for it.

---

## API

```go
func NewBuffer(chunkSize ...int) *Buffer

func (b *Buffer) Write(p []byte) (int, error)
func (b *Buffer) WriteString(s string) (int, error)
func (b *Buffer) WriteByte(c byte) error
func (b *Buffer) Read(p []byte) (int, error)
func (b *Buffer) ReadByte() (byte, error)
func (b *Buffer) ReadFrom(r io.Reader) (int64, error)
func (b *Buffer) WriteTo(w io.Writer) (int64, error)

func (b *Buffer) Bytes() []byte
func (b *Buffer) String() string
func (b *Buffer) Len() int
func (b *Buffer) Cap() int
func (b *Buffer) Grow(n int)

func (b *Buffer) Reset()          // keep blocks for reuse
func (b *Buffer) Close()          // release blocks to the pool
func (b *Buffer) SetMaxSize(n int64)
```

### The zero value works

```go
var b chunkbuffer.Buffer
b.Write([]byte("works"))
```

### `Reset` keeps memory, `Close` releases it

`Reset` truncates the buffer but keeps the blocks, so refilling does not
allocate. `Close` returns every block to the shared pool; it is idempotent and
the buffer remains usable afterwards.

Note that `Reset` retains *all* blocks: the buffer keeps its peak footprint.
A buffer that once held 64 MiB still holds 64 MiB after `Reset`, and that
memory is not reclaimed by the GC while the buffer is alive (blocks in the
shared pool are). This mirrors `bytes.Buffer`, whose `Reset` also keeps its
full array — but if a long-lived buffer is fed by spiky payloads, call `Close`
after an oversized one so the memory can be reused elsewhere:

```go
b := chunkbuffer.NewBuffer()
// ... write a large payload ...
b.WriteTo(dst)
b.Close() // return the blocks
```

---

## Behaviour worth knowing

- **`Read` follows the `io.Reader` convention:** a non-zero `n` always comes
  with a `nil` error. `io.EOF` only appears when the buffer was already empty.
- **`Bytes` returns a copy**, unlike `bytes.Buffer`, which returns an alias
  into its internal slice. Mutating the result cannot corrupt the buffer, and
  the result stays valid after the buffer is reused. The cost is one
  allocation; use `WriteTo` or `Read` if you need zero-copy.
- **Not safe for concurrent use.** Like `bytes.Buffer`, synchronise externally.
  Blocks are recycled through shared per-size-class pools, so a data race here
  can surface as data from another buffer.
- **Chunk size is a throughput/memory dial.** Blocks are pooled per power-of-two
  size class, so recycling works at any chunk size up to 8 MiB. Larger blocks
  mean fewer indirections: with a 250 KiB payload, write+read+reset measured
  ~9.0 µs/op at 256 KiB chunks versus ~9.8 µs/op at the default 8 KiB — on par
  with `bytes.Buffer`. The price is a larger minimum memory floor per block, so
  buffers that stay small should keep the default.
- **`SetMaxSize` panics with `ErrTooLarge`** rather than returning an error,
  because exceeding a limit you configured is a programming error.
- **Memory is not wiped on reuse.** Recycling does not zero blocks; instead
  every read path is bounded by `len`, so stale bytes are never observable.
  This is enforced by [`TestNoMemoryDisclosure`](buffer_test.go). If you need
  defence against heap inspection of freed memory, this package does not
  provide it — use explicit zeroing or a different design.
- **`Cap()` reports the capacity actually held**, which is not simply
  `Len()` rounded up.

---

## Migrating from v0.0.1

v1.0.0 fixes several correctness bugs; the API changes below were necessary to
do that. See [CHANGELOG.md](CHANGELOG.md) for the full list.

| v0.0.1 | v1.0.0 |
|---|---|
| `var ChunkSize int` (global) | `NewBuffer(size)` or `DefaultChunkSize` |
| `NewBuffer()` | `NewBuffer()` or `NewBuffer(size int)` |
| `Reset() error` | `Reset()` |
| `Close() error` | `Close()` |
| `Read` returned `(n>0, io.EOF)` | returns `nil` when `n > 0` |
| `Bytes()` returned an alias | returns a copy |

If you were relying on `WriteTo`, upgrade immediately: in v0.0.1 it emitted
whole blocks regardless of `Len()`, producing garbage bytes and a negative
`Len()`.

---

## Slab Arena & Hybrid ArenaBuffer

For ultra-low latency event loops and worker threads, `chunkbuffer` provides a **1-cycle bitmask slab arena** (`SlabArena`) and a hybrid buffer (`ArenaBuffer`):

```go
arena := chunkbuffer.NewSlabArena()
buf := chunkbuffer.NewArenaBuffer(arena)
defer buf.Close()

buf.Write(payload)
// Up to 512 KB is served in O(1) time (~1.7 ns) directly from the 2 MB slab arena.
// Payloads exceeding 512 KB seamlessly spill over into a pooled chunkbuffer.Buffer.
```

- **1-Cycle Allocation & Free:** Allocates and frees 32 KB blocks in 1-2 CPU clock cycles using hardware `TZCNT` (`bits.TrailingZeros64`).
- **GC Invisible (`noscan`):** The entire 2 MB slab is a single pointerless byte slice, completely invisible to the Go GC mark phase.
- **Zero-Copy Block Traversal:** Supports `VisitChunks`, `Chunks()`, `AppendChunks`, and `CopyTo` for scatter-gather I/O (`writev`).

---

## Testing

```bash
go test -race ./...
go test -fuzz FuzzBuffer -fuzztime 30s
go test -bench . -benchmem
```

The suite includes a differential test that replays random write/read
interleavings against `bytes.Buffer`, two fuzz targets, and targeted
regression tests for each bug listed in the changelog.

## License

MIT. See [LICENSE](LICENSE), which also covers portions derived from the Go
standard library.