# Development entrypoints for pulumi-tool-import.
#
# Common targets:
#   make build   - compile the CLI
#   make test    - run the Go test suite
#   make lint    - run golangci-lint
#   make fmt     - format the tree (gofmt)
#   make tidy    - go mod tidy
#   make check   - fmt-check + vet + lint (what CI enforces, minus the integration tests)

GO      ?= go
BINARY  ?= pulumi-tool-import
PKG     ?= ./...

.PHONY: all build test lint fmt fmt-check vet tidy check clean

all: build

build:
	$(GO) build -o bin/$(BINARY) .

test:
	$(GO) test $(PKG)

lint:
	golangci-lint run

# gofmt the whole tree in place.
fmt:
	gofmt -w .

# Fail if any file is not gofmt-clean (used by CI).
fmt-check:
	@unformatted="$$(gofmt -l . | grep -v -E '^(docs)/' || true)"; \
	if [ -n "$$unformatted" ]; then \
		echo "The following files are not gofmt-clean:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

vet:
	$(GO) vet $(PKG)

tidy:
	$(GO) mod tidy

check: fmt-check vet lint

clean:
	rm -rf bin dist
