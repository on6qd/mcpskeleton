GO      ?= go
BIN     ?= bin/mcpskeleton
PKG     := ./...

.PHONY: all build test race vet lint fmt tidy run clean check

all: check build

build:
	$(GO) build -o $(BIN) ./cmd/mcpskeleton

test:
	$(GO) test $(PKG)

race:
	$(GO) test -race $(PKG)

vet:
	$(GO) vet $(PKG)

lint:
	@command -v golangci-lint >/dev/null 2>&1 \
		&& golangci-lint run \
		|| echo "golangci-lint not installed; skipping (brew install golangci-lint)"

fmt:
	$(GO) fmt $(PKG)

tidy:
	$(GO) mod tidy

check: vet lint test

run: build
	$(BIN) serve

clean:
	rm -rf bin
