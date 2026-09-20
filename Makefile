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
# Which sources decide which compiled file. The artifact needs no rule here: it carries
# the build that wrote it and `retsin artifact check` answers whether this binary can read
# it, which is the same comparison the server makes before it serves anything. The index
# carries no stamp at all — its provision ids are only meaningful against the artifact they
# were cut with — so a mtime rule is the only thing that notices, and it is a guess. The
# server will not complain if the guess is wrong, which is the weakest link in this file.
# cmd/ is deliberately not in the list: the command wires the build up, the packages below are
# what cut a document into passages, and leaving cmd/ in would rebuild the index for every flag
# edit and teach everyone to ignore the deploy output.
INDEX_SRC := internal/search internal/lexdania internal/lexdoc

# This machine's go env file and module sumdb path are read-only, so module
# verification must be off whenever dependencies change. Existing deps are
# already recorded in go.sum, so plain builds need nothing extra.
GOSUMDB ?= off

.PHONY: all build test vet fmt tidy clean deploy

all: vet test build

build:
	@mkdir -p bin
	$(GO) build -o $(BIN) $(PKG)

# Deploy is compile, replace, recycle. The artifact check is exact because the artifact is
# stamped with its own build; the index check is a mtime guess because the index is not, and
# nothing downstream complains when it is wrong.
deploy: build
	@if $(BIN) artifact check --data-dir $(DATA); then \
	  true; \
	else echo "this build cannot serve the artifact on disk: rebuilding it"; \
	  $(BIN) artifact build --data-dir $(DATA); fi
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
