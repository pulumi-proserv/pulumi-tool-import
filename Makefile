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
# Credentials come from Pulumi ESC via the CE demo account, never a customer
# account. "env -u AWS_PROFILE" is required: the developer shell exports
# AWS_PROFILE=devsandbox, which shadows the credentials "esc run" injects.
# Without PULUMI_ACCESS_TOKEN or AWS credentials, the test skips cleanly
# rather than failing.
test-e2e:
	PULUMI_ACCESS_TOKEN=$$JDAVENPORT_PULUMI_CORP_PULUMI_ACCESS_TOKEN \
		esc run team-ce/aws/pulumi-ce -- \
		env -u AWS_PROFILE $(GO) test -tags e2e ./test/e2e/ -v -timeout 40m

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
