GO      ?= go
BIN     := retsin
PKG     := ./cmd/retsin

# This machine's go env file and module sumdb path are read-only, so module
# verification must be off whenever dependencies change. Existing deps are
# already recorded in go.sum, so plain builds need nothing extra.
GOSUMDB ?= off

.PHONY: all build test vet fmt tidy clean

all: vet test build

build:
	$(GO) build -o $(BIN) $(PKG)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w cmd internal

tidy:
	GOSUMDB=$(GOSUMDB) $(GO) mod tidy

clean:
	rm -f $(BIN)
