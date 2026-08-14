# Development entrypoints for pulumi-tool-import.
#
# Common targets:
#   make build     - compile the CLI
#   make test      - run the Go test suite
#   make test-e2e  - run the AWS end-to-end test (needs ESC credentials; see below)
#   make lint      - run golangci-lint
#   make fmt       - format the tree (gofmt)
#   make tidy      - go mod tidy
#   make check     - fmt-check + vet + lint (what CI enforces, minus the integration tests)

GO      ?= go
BINARY  ?= pulumi-tool-import
PKG     ?= ./...
E2E_TIMEOUT ?= 40m

.PHONY: all build test test-e2e lint fmt fmt-check vet tidy check clean

all: build

build:
	$(GO) build -o bin/$(BINARY) .

test:
	$(GO) test $(PKG)

# test-e2e creates and destroys real AWS infrastructure (a VPN gateway,
# route tables, a VPN connection) to prove non-importable resources go from
# "create" to "same" across "patch-state tf --non-importable" — see
# test/e2e/e2e_test.go for what it proves and why "pulumi refresh" cannot be
# used for this.
#
# Runs the AWS end-to-end test for state injection. It creates real
# infrastructure, so it is not part of "make test" and is tagged "e2e".
#
# This target deliberately knows nothing about credentials: supply AWS
# credentials in the environment however you normally do, and wrap the
# invocation if you use a credential broker. With Pulumi ESC, for example:
#
#   esc run <your-aws-environment> -- env -u AWS_PROFILE make test-e2e
#
# Two things that commonly trip this up:
#   - "env -u AWS_PROFILE" is needed if your shell exports AWS_PROFILE, since
#     it shadows brokered credentials.
#   - "esc run" needs a Pulumi Cloud login; it reads the stored login rather
#     than PULUMI_ACCESS_TOKEN or PULUMI_BACKEND_URL, so a file:// login fails
#     with "does not support Pulumi ESC".
#
# The test skips cleanly when credentials are absent, always destroys what it
# creates, and logs the account it is using. Choosing the right account is
# yours to get right.
test-e2e:
	$(GO) test -tags e2e ./test/e2e/ -v -timeout $(E2E_TIMEOUT)

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
