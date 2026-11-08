# Agent Guidelines for chunkbuffer

> Machine-readable operating contract for AI coding agents (Antigravity, Claude Code, Cursor, Copilot, Aider) working on this repository.
> Keep this file accurate: an agent that follows stale instructions is worse than an agent with no instructions.

---

## 1. Architecture & Mental Model

- **What this is**: A high-performance byte buffer for Go that stores contents in fixed-size blocks (and optional 2 MB bitmask SlabArena) instead of one contiguous slice, eliminating allocation growth spikes.
- **Core design decisions**:
  - **No Contiguous Reallocations**: `Buffer` appends pooled blocks (default 8 KiB up to 8 MiB) and never reallocates the entire buffer during growth, allocating ~64 MiB total when growing to 64 MiB (vs ~127 MiB for `bytes.Buffer`).
  - **Zero Memory Disclosure**: Stale chunk contents are never zeroed on recycling for maximum throughput, but every read and slice path is strictly bounded by active byte length (`b.n`).
  - **1-Cycle Slab Arena**: Worker-local 2 MB contiguous slab arena (64 blocks of 32 KB) tracked with a single `uint64` bitmask using hardware `TZCNT` (`bits.TrailingZeros64`), invisible to GC (`noscan`).
  - **Single-Threaded Core**: Like `bytes.Buffer`, `Buffer` and `ArenaBuffer` are not safe for concurrent use; synchronise externally.
- **Module map**:
  | Path | Responsibility |
  | :--- | :--- |
  | `chunkbuffer.go` | Core `Buffer`, size-class chunk pooling, stdlib `io` interfaces, buffer pooling |
  | `arena.go` | 2 MB `SlabArena` bitmask allocator, `ArenaBuffer` hybrid buffer, zero-copy chunk visitor |
  | `buffer_test.go` | Unit, regression, differential, and security tests |
  | `bench_test.go` | High-throughput benchmark suite comparing against standard library `bytes.Buffer` |
  | `fuzz_test.go` | Go native fuzz targets (`FuzzBuffer`, `FuzzReadFrom`) |

---

## 2. Quality Gates (MUST pass before proposing any change)

Run these exact commands from the repository root:

```bash
gofmt -l .             # Formatting check: must output NOTHING
go vet ./...           # Static analysis: must exit 0 with 0 warnings
go test -race ./...    # Test suite: must pass with race detector ON
go build ./...         # Build check: must exit 0
```

If any gate fails, fix the regression **before** adding new behavior.

---

## 3. Hard Invariants (never violate)

- ❌ **DO NOT**: Add external dependencies (`chunkbuffer` has a strict zero-dependency invariant).
- ❌ **DO NOT**: Return an alias from `Bytes()`; `Bytes()` must return an isolated copy (use `Chunks()`, `VisitChunks()`, or `WriteTo()` for zero-copy access).
- ❌ **DO NOT**: Violate `io.Reader` conventions (non-zero `n` must always return `nil` error; `io.EOF` only when empty).
- ❌ **DO NOT**: Commit secrets, credentials, or private configuration files.
- ✅ **ALWAYS**: Run `gofmt -w .` on any modified Go files.
- ✅ **ALWAYS**: Add a regression or unit test for every bug fix or feature.
- ✅ **ALWAYS**: Keep `README.md` and `CHANGELOG.md` updated when APIs change.

---

## 4. Canonical Snippets

Reference usage patterns that compile cleanly:

```go
package main

import (
	"fmt"

	"github.com/vintik100/chunkbuffer"
)

func main() {
	// Standard buffer with pooled chunks
	buf := chunkbuffer.NewBuffer()
	defer buf.Close()

	buf.Write([]byte("hello, "))
	buf.WriteString("world")
	fmt.Println(buf.String())

	// High-throughput slab arena buffer
	arena := chunkbuffer.NewSlabArena()
	aBuf := chunkbuffer.NewArenaBuffer(arena)
	defer aBuf.Close()

	aBuf.WriteString("fast O(1) slab writes")
}
```

---

## 5. Contribution Workflow

- Branch from `main`: `git checkout -b feat/<name>` or `fix/<name>`.
- Follow [Conventional Commits](https://www.conventionalcommits.org/).
- See `CONTRIBUTING.md` for PR guidelines and `SECURITY.md` for vulnerability reports.
