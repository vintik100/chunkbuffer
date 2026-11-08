# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `SlabArena`: A worker-local 2 MB contiguous slab arena (64 blocks of 32 KB) tracked with a single 64-bit word (`uint64`). Allocations and deallocations execute in 1-2 CPU clock cycles using hardware `TZCNT` (`bits.TrailingZeros64`). The entire backing memory is a single pointerless byte slice, completely invisible to the Go GC mark phase (`noscan`).
- `ArenaBuffer`: A hybrid multi-block buffer backed by `SlabArena` that holds up to 16 blocks (512 KB) in O(1) time with transparent spillover to pooled `Buffer` instances for larger payloads.
- `AcquireBuffer` and `ReleaseBuffer`: Global buffer pooling for `Buffer` structs with a 128 KiB smart capacity retention ceiling to prevent pool thrashing and memory leakage.
- `VisitChunks`, `Chunks`, `AppendChunks`, and `CopyTo`: Zero-copy block traversal methods for scatter-gather I/O (`writev`).

- `WriteString(string) (int, error)` implementing `io.StringWriter` with zero heap allocations.
- `WriteByte(byte) error` and `ReadByte() (byte, error)` implementing `io.ByteWriter` and `io.ByteReader`.
- `Grow(int)` to preallocate capacity, for parity with `bytes.Buffer.Grow`. Note
  that it is not a speed optimisation for bulk writes: in
  `BenchmarkGrowPreallocated` the preallocated path measures ~3% slower and
  ~32% more B/op than plain `Write` growth, because blocks make an extra round
  trip through the internal free list. Use it when you need the capacity
  guarantee up front (e.g. before a latency-sensitive section), not to make
  growth faster.

### Fixed

- **Cross-size pool contamination**: recycled blocks of every capacity shared
  a single pool, so a buffer could inherit a block far larger than its chunk
  size — a 4 KiB buffer holding 4 bytes could report `Cap()` of 1 MiB after an
  unrelated buffer released 1 MiB blocks. Blocks are now recycled through one
  pool per power-of-two size class (`DefaultChunkSize` up to 8 MiB), so every
  class stays homogeneous while custom chunk sizes keep recycling their own
  blocks. Blocks larger than the biggest class are allocated exactly and never
  pooled. Covered by `TestPoolHygiene` and `TestCustomSizeChunkPooling`.

### Optimized

- **Fast-path inlining for `WriteByte` and `ReadByte`**: Separates the single-byte operations into an inlinable fast path for the common case (when the current block has available room or unread bytes) and a slow path for block exhaustion or allocation.
- **Streamlined `Write` and `WriteString` chunk loops**: Writes now fill the existing tail block once upfront and process all subsequent fresh blocks directly, eliminating repeated `b.tail()` function calls, bounds checks, and redundant room recalculations. This drops write latency on large payloads by ~7%, matching or surpassing `bytes.Buffer` in write benchmarks.
- **`Reset` block retention**: Reusable capacity is preserved internally across
  `Reset()` instead of returning blocks to `sync.Pool`, eliminating pool
  contention. Measured with benchstat (n=6) against v1.0.0: `BenchmarkWrite`
  (write + reset, 250 KiB) is ~11% faster (p=0.002). Trade-off: `Reset` now
  retains *all* blocks, so a buffer that once held a large payload pins that
  peak memory until `Close` — unlike pooled blocks, retained blocks are not
  reclaimed by the GC. See the README section on `Reset`/`Close`.
- **Instant cursor reset on full drain**: When all chunks are consumed (`head == len(chunks)`), `dropHead` immediately resets `chunks` to `[:0]` and `head = 0`, eliminating slice drift and compaction overhead in streaming loops.

## [1.0.0]

First stable release. The previous tag, `v0.0.1` (July 2020), was an
experiment and contained several correctness bugs; this release rewrites the
core and changes the API where the old one made correctness impossible.

### Fixed

- **`WriteTo` corrupted data and returned a negative `Len()`.** It sliced
  blocks as `B[ci:free]`, mixing an index with a length, and never bounded the
  write to `Len()`. A 3-byte buffer produced 8192 bytes of output and left
  `Len()` at -8189; a non-block-aligned read position produced
  `io.ErrShortWrite` and silently dropped data. `WriteTo` now respects `Len()`
  exactly.
- **Information disclosure across buffers.** Recycled blocks kept their
  previous contents, and `WriteTo` could emit them. All read paths are now
  strictly bounded by `len`, and `TestNoMemoryDisclosure` covers it.
- **Unbounded memory growth while streaming.** The write and read cursors only
  ever increased, so a buffer used in a write-then-read loop retained every
  block it had ever written: 8 MB for a 4 KB working set after 2000 cycles.
  Consumed blocks are now released as they are read.
- **Panic on the zero value.** `Bytes()` and `String()` skipped block-size
  initialisation and divided by zero on a `var b Buffer`.
- **Panic from a mutable global `ChunkSize`.** Changing it while blocks were in
  the pool made recycled blocks the wrong size, causing slice bounds panics.
  Block size is now per-buffer.
- **`Close` corrupted the pool when called twice.** It returned the same blocks
  twice, so `sync.Pool` could hand one block to two live buffers. `Close` is
  now idempotent.
- **`Read` returned `(n>0, io.EOF)`**, violating the `io.Reader` convention and
  breaking the common read-then-check-error loop. It now returns a `nil` error
  whenever `n > 0`.
- **`ErrTooLarge` was declared but never used**, while the `ReadFrom`
  documentation promised it would be. `SetMaxSize` now enforces a real limit.

### Changed

- `var ChunkSize int` removed; use `NewBuffer(size)` or `DefaultChunkSize`.
- `Reset() error` → `Reset()`; `Close() error` → `Close()`. Neither ever
  returned a non-nil error.
- `Bytes()` now returns a copy instead of an alias into internal storage.
- `Reset` keeps blocks for reuse; `Close` releases them to the pool.
- Minimum Go version is 1.21.

### Added

- `NewBuffer(chunkSize ...int)`, `SetMaxSize(int64)`, `ErrNegativeRead`.
- Differential tests against `bytes.Buffer`, two fuzz targets, targeted
  regression tests for each bug above, and benchmarks comparing against
  `bytes.Buffer` on identical work.
- `TestPeakAllocation`, which asserts the property the package exists for:
  growing to 64 MiB allocates ~64 MiB here versus ~127 MiB with `bytes.Buffer`.
- README, LICENSE, CHANGELOG and CI (gofmt, vet, staticcheck, race, fuzz).

[Unreleased]: https://github.com/vintik100/chunkbuffer/compare/v1.1.0...main
[1.1.0]: https://github.com/vintik100/chunkbuffer/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/vintik100/chunkbuffer/compare/v0.0.1...v1.0.0
