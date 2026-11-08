# Contributing Guidelines

Thank you for considering contributing to `chunkbuffer`! We welcome bug reports, documentation improvements, performance optimizations, and code contributions.

---

## Code of Conduct

By participating in this project, you agree to maintain a respectful and welcoming environment for everyone. Please be constructive, considerate, and professional.

---

## Development Setup

1. **Fork and Clone**:
   ```bash
   git clone https://github.com/vintik100/chunkbuffer.git
   cd chunkbuffer
   ```

2. **Branching Model**:
   - Create a feature or bugfix branch off `main`:
     ```bash
     git checkout -b feat/your-feature-name
     # or
     git checkout -b fix/issue-description
     ```

3. **Running Quality Checks**:
   Always ensure tests, linters, and formatters pass cleanly before opening a Pull Request:
   ```bash
   # Formatting check (must output nothing)
   gofmt -l .

   # Static analysis (must exit 0 with 0 warnings)
   go vet ./...

   # Full test suite with race detector
   go test -race ./...

   # Fuzz tests
   go test -run XXX -fuzz FuzzBuffer -fuzztime 15s ./...

   # Benchmarks
   go test -bench . -benchmem
   ```

---

## Commit Guidelines

We recommend using [Conventional Commits](https://www.conventionalcommits.org/):
- `feat: add lock-free chunk reservation`
- `fix: resolve race condition in pool recycle`
- `docs: update benchmark table for AMD Ryzen`
- `perf: streamline write loop fast path`
- `test: add regression test for buffer overflow`

---

## Pull Request Process

1. Ensure your code compiles cleanly and zero new warnings/lint errors are introduced.
2. If introducing a bug fix or new invariant, include a corresponding regression test in `buffer_test.go` or `arena_test.go`.
3. For performance-critical changes, attach before/after benchmark results (`benchstat` or equivalent).
4. Update `README.md` and `CHANGELOG.md` if your change modifies public APIs or behaviors.
5. Fill out the PR template with clear context and rationale.
