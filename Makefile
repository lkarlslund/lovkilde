GO      ?= go
BIN     := bin/lovkilde
PKG     := ./cmd/lovkilde
UPDATER := bin/lovkilde-update
UPDPKG  := ./cmd/lovkilde-update
SERVICE := lovkilde.service
DATA    := data

# The service runs this file where it is built, the way the llama.cpp and ninfer units
# run their own build output. There is no install copy, because a copy is a second file
# that can be older than the one you just compiled — which is exactly how a deploy came
# to install a two-week-old binary and report success.
#
# Which sources decide which compiled file. The artifact needs no rule here: it carries
# its document model version and `lovkilde artifact check` answers whether this binary can read
# it, which is the same comparison the server makes before it serves anything. The index
# carries no stamp at all — its provision ids are only meaningful against the artifact they
# were cut with — so a mtime rule is the only thing that notices, and it is a guess. The
# server will not complain if the guess is wrong, which is the weakest link in this file.
# cmd/ is deliberately not in the list: the command wires the build up, the packages below are
# what cut a document into passages, and leaving cmd/ in would rebuild the index for every flag
# edit and teach everyone to ignore the deploy output.

# This machine's go env file and module sumdb path are read-only, so module
# verification must be off whenever dependencies change. Existing deps are
# already recorded in go.sum, so plain builds need nothing extra.
GOSUMDB ?= off

.PHONY: all build test vet fmt tidy clean deploy

all: vet test build

# Two programs: lovkilde serves an artifact and reads nothing else; lovkilde-update collects the
# documents from retsinformation.dk and compiles the artifact.
# The release number, rNNNNN, is the number of commits on the branch built.
RELEASE := r$(shell printf %05d $$(git rev-list --count HEAD 2>/dev/null || echo 0))

build:
	@mkdir -p bin
	$(GO) build -ldflags "-X main.version=$(RELEASE)" -o $(BIN) $(PKG)
	$(GO) build -o $(UPDATER) $(UPDPKG)

# Deploy is compile, replace, recycle. The artifact check is exact because the artifact is
# stamped with its document model. The search index is not built here: the server keeps it,
# rebuilding it from the artifact at startup when it is missing or was cut from another artifact
# (a minute, during which lookups answer and search says it is on its way).
deploy: build
	@if $(BIN) artifact check --data-dir $(DATA); then \
	  true; \
	else echo "this build cannot serve the artifact on disk: rebuilding it"; \
	  $(UPDATER) artifact build --data-dir $(DATA); fi
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
