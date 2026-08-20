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
#                    vet and lint each also cover the "e2e"-tagged build, which is
#                    otherwise invisible to every target and CI job.

GO      ?= go
BINARY  ?= pulumi-tool-import
PKG     ?= ./...
E2E_TIMEOUT ?= 40m

.PHONY: all build test test-e2e lint lint-e2e fmt fmt-check vet vet-e2e tidy check clean

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
	# -count=1 defeats the test cache. Without it, a second invocation with an
	# unchanged tree replays the previous run's stored output and prints
	# "ok ... (cached)" without touching AWS at all — a stale pass that reads
	# exactly like a fresh one, for a test whose entire purpose is to exercise
	# real infrastructure.
	$(GO) test -count=1 -tags e2e ./test/e2e/ -v -timeout $(E2E_TIMEOUT)

lint: lint-e2e
	golangci-lint run

# The e2e files are behind "//go:build e2e", so the untagged run above does not
# see them at all -- 2,500+ lines that were never linted, vetted or even
# type-checked by any target or CI job. That is not hypothetical: two real bugs
# lived in test/e2e/orphan_check.go (an ordering error that disabled the whole
# AWS-side orphan sweep on the success path, and an error-code match that could
# never fire for IAM), and neither the offline suite nor a green e2e run could
# surface them. Linting the tagged build costs seconds and needs no AWS.
lint-e2e:
	golangci-lint run --build-tags e2e ./test/e2e/...
	golangci-lint run --build-tags deltasweep ./pkg/...
	golangci-lint run --build-tags providerload ./pkg/tfprovider/... ./pkg/bridgedproviders/...

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

vet: vet-e2e
	$(GO) vet $(PKG)

# Type-check and vet the e2e build. "go vet" fully type-checks, so this is what
# catches a compile error in a tagged file before someone spends ~16 minutes and
# real AWS spend discovering it. See lint-e2e for why this is separate.
vet-e2e:
	$(GO) vet -tags e2e ./test/e2e/...
	$(GO) vet -tags deltasweep ./pkg/...
	$(GO) vet -tags providerload ./pkg/tfprovider/... ./pkg/bridgedproviders/...

tidy:
	$(GO) mod tidy

check: fmt-check vet lint

clean:
	rm -rf bin dist
