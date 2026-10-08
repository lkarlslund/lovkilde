#!/usr/bin/env bash
# The weekly Hugging Face release, from a fresh checkout: a GitHub runner
# (.github/workflows/hf-release.yml) or any machine with Go and the hf command.
#
#   HF_REPO=owner/name HF_TOKEN=hf_... scripts/hf-release.sh
#
# 1. restore the published collection state and artifact (and its base, when it is a layer)
# 2. update: harvest the change feed and fetch what changed since the published pass
# 3. build the next artifact as a layer over the published one; when the published artifact
#    cannot be continued (another document model or dictionary), unpack the published XML
#    archive and build in full
# 4. publish the artifact, the state and the XML packs this week touched, in one commit
#
# MODE=patch writes a whole artifact instead of a layer; MODE=full always builds in full.
#
# The data directory is ./data, as on the machine that published the first release: a
# document records its sidecar's path under the archive root, so an incremental build refuses
# an artifact built from another one.
#
# The first release is published from a machine holding the whole archive, once:
#   hf repo create owner/name --repo-type dataset
#   bin/retsin-update artifact build
#   bin/retsin-update publish --release hf://owner/name --with-xml
set -euo pipefail

: "${HF_REPO:?set HF_REPO to owner/name or hf://owner/name}"
# The script publishes to Hugging Face only; a bare owner/name would otherwise name a directory.
case "$HF_REPO" in hf://*) ;; *) HF_REPO="hf://$HF_REPO" ;; esac
: "${HF_TOKEN:?set HF_TOKEN to a token with write access to the repository}"
mode="${MODE:-layer}"
case "$mode" in layer | patch | full) ;; *) echo "MODE is layer, patch or full, not $mode" >&2; exit 2 ;; esac

cd "$(dirname "$0")/.."
data=data
mkdir -p bin
go build -o bin/retsin-update ./cmd/retsin-update
u() { bin/retsin-update --data-dir "$data" --progress never "$@"; }

echo "== restore from $HF_REPO"
if ! u restore --release "$HF_REPO" --with-artifact; then
  # The state is restored before the artifact; an artifact this build cannot read (another
  # document model) means building in full.
  [ -f "$data/state/meta.json" ] || exit 1
  echo "the published artifact cannot be continued: building in full"
  mode=full
fi

echo "== update"
u update

echo "== build ($mode)"
if [ "$mode" = full ] || ! u artifact build --mode "$mode"; then
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    # The archive unpacks to 14 GB; a hosted runner has room for it once the preinstalled
    # toolchains this job does not use are gone.
    sudo rm -rf /usr/share/dotnet /usr/local/lib/android /opt/ghc /opt/hostedtoolcache/CodeQL || true
    df -h .
  fi
  u restore --release "$HF_REPO" --xml-only
  u artifact build --mode full
fi

echo "== publish to $HF_REPO"
u publish --release "$HF_REPO" --with-xml
u status
