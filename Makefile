GO      ?= go
BIN     := bin/retsin
PKG     := ./cmd/retsin
SERVICE := retsin.service
DATA    := data

# The service runs this file where it is built, the way the llama.cpp and ninfer units
# run their own build output. There is no install copy, because a copy is a second file
# that can be older than the one you just compiled — which is exactly how a deploy came
# to install a two-week-old binary and report success.
#
# Which sources decide which compiled file: the artifact is the document model, so
# anything that builds or encodes it changes what the next artifact should say; the index
# is that model cut into passages, so the unit-cutting and matching packages decide it.
# The server refuses an artifact from another build, but nothing pins the index, so this
# file is the only place that keeps its provision ids in step with the artifact's.
MODEL_SRC := internal/lexdoc internal/compiled internal/lexdania cmd/retsin
INDEX_SRC := internal/search internal/lexdania internal/lexdoc cmd/retsin

# This machine's go env file and module sumdb path are read-only, so module
# verification must be off whenever dependencies change. Existing deps are
# already recorded in go.sum, so plain builds need nothing extra.
GOSUMDB ?= off

.PHONY: all build test vet fmt tidy clean deploy

all: vet test build

build:
	@mkdir -p bin
	$(GO) build -o $(BIN) $(PKG)

# Deploy is compile, replace, recycle. The two rebuild checks are time-saving, not
# safety: if either guess is wrong the server says so at startup, because it refuses an
# artifact written by another build rather than reading it as today's model.
deploy: build
	@if [ ! -f $(DATA)/artifact.bin ] || [ -n "$$(find $(MODEL_SRC) -name '*.go' -newer $(DATA)/artifact.bin | head -1)" ]; then \
	  echo "artifact is older than the code that writes it: rebuilding"; \
	  $(BIN) artifact build --data-dir $(DATA); \
	else echo "artifact is newer than the code that writes it"; fi
	@if [ ! -f $(DATA)/search.idx ] || [ -n "$$(find $(INDEX_SRC) -name '*.go' -newer $(DATA)/search.idx | head -1)" ]; then \
	  echo "index is older than the code that cuts its units: rebuilding"; \
	  $(BIN) index build --data-dir $(DATA); \
	else echo "index is newer than the code that cuts its units"; fi
	systemctl --user daemon-reload
	systemctl --user restart $(SERVICE)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w cmd internal

tidy:
	GOSUMDB=$(GOSUMDB) $(GO) mod tidy

clean:
	rm -rf bin
