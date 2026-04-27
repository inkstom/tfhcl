# Contributing to tfhcl

Thanks for your interest in contributing! This is a small, focused project, so
patches that align with the existing scope are much easier to land.

## Development setup

```sh
git clone https://github.com/you/tfhcl
cd tfhcl
go build ./...
go test ./...
```

The project depends only on Go (>= 1.26) and a few open-source libraries. No
build system beyond the included `Makefile`.

## Common tasks

```sh
make build        # compile the binary into ./tfhcl
make test         # run unit tests
make test-race    # run tests with -race
make coverage     # produce coverage.html
make fmt          # gofmt -s -w
make vet          # go vet
make lint         # golangci-lint (install: https://golangci-lint.run)
make check        # fmt + vet + test + lint
```

## Submitting changes

1. Open an issue first for anything beyond a small fix — it's the cheapest
   way to find out whether the change fits.
2. One change per pull request. Keep diffs reviewable.
3. Add or update tests for any behavior change. The CI pipeline runs `go
   test -race`, `go vet`, `gofmt`, and `golangci-lint` — please run
   `make check` locally before pushing.
4. Follow standard Go style. Comments on exported identifiers should explain
   *why* the thing exists, not restate its name.
5. Keep dependencies minimal. Adding a new direct dependency needs a
   justification in the PR description.

## Reporting bugs

Open an issue with:

- A minimal `.tf` snippet that reproduces the problem
- The exact `tfhcl` command you ran
- The expected output and what actually happened
- Your `tfhcl --help` output (or commit SHA if built from source)

## Areas where help is welcome

- More test coverage, especially golden-file tests for `--plan` output
- Selector syntax extensions (e.g. negation, intersection)
- Output formats (`--json`, `--yaml`)
- Better terminal-resize handling in the TUI

## Code of conduct

Be kind. Assume good intent. Disagreements about technical direction are
fine; personal attacks are not.
