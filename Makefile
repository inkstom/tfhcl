.PHONY: build test test-race coverage lint fmt vet check install clean tidy help

GO      ?= go
BIN     ?= tfhcl
PKG     ?= ./...
LDFLAGS ?= -s -w

help:
	@echo "Targets:"
	@echo "  build       Compile the tfhcl binary"
	@echo "  test        Run unit tests"
	@echo "  test-race   Run tests with the race detector"
	@echo "  coverage    Run tests and open an HTML coverage report"
	@echo "  fmt         Format Go sources"
	@echo "  vet         Run go vet"
	@echo "  lint        Run golangci-lint"
	@echo "  check       fmt + vet + test + lint"
	@echo "  install     Install the binary into GOBIN"
	@echo "  tidy        Tidy go.mod / go.sum"
	@echo "  clean       Remove build artifacts and .bak files"

build:
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)

test:
	$(GO) test -count=1 $(PKG)

test-race:
	$(GO) test -race -count=1 $(PKG)

coverage:
	$(GO) test -race -count=1 -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "coverage report: coverage.html"

fmt:
	$(GO) fmt $(PKG)
	@command -v gofmt >/dev/null && gofmt -s -w .

vet:
	$(GO) vet $(PKG)

lint:
	@command -v golangci-lint >/dev/null || { echo "golangci-lint not installed; see https://golangci-lint.run"; exit 1; }
	golangci-lint run

check: fmt vet test lint

install:
	$(GO) install $(PKG)

tidy:
	$(GO) mod tidy

clean:
	rm -f $(BIN) coverage.out coverage.html
	find . -name '*.bak' -type f -delete
