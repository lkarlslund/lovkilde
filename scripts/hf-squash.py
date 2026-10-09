#!/usr/bin/env python3
"""Squash the release dataset's history into one commit holding the current release.

    HF_REPO=owner/name HF_TOKEN=hf_... scripts/hf-squash.py [--dry-run]

Every daily release is a commit, and the Hub keeps every file any commit ever named, so the
dataset grows by the artifact's changed chunks and the touched packs each day.
A release is whatever latest.json on main names, so squashing changes nothing for a server or
for the next release; it only gives up fetching an earlier release by commit.

Afterwards it reports the large files the Hub still stores that the current files do not use,
which is the space the squash should let go of. It never deletes them: the Hub's
permanently_delete_lfs_files affects every commit naming a file, and whether a squash frees
them by itself is to be seen from this report.

Run it only between releases: the release workflow runs it after publishing, in the same job.
"""
import os
import sys

from huggingface_hub import HfApi


def main() -> int:
    repo = os.environ.get("HF_REPO", "").removeprefix("hf://")
    if not repo or "@" in repo:
        print("set HF_REPO to owner/name (the main branch is squashed)", file=sys.stderr)
        return 2
    dry = "--dry-run" in sys.argv[1:]
    api = HfApi()
    commits = api.list_repo_commits(repo, repo_type="dataset")
    print(f"{repo}: {len(commits)} commits")
    if not dry and len(commits) > 1:
        api.super_squash_history(repo, repo_type="dataset", commit_message="Squash the release history")
        print(f"squashed {len(commits)} commits into one")

    current = {
        f.lfs.sha256
        for f in api.list_repo_tree(repo, repo_type="dataset", recursive=True, expand=True)
        if getattr(f, "lfs", None)
    }
    stored = list(api.list_lfs_files(repo, repo_type="dataset"))
    unused = [f for f in stored if f.file_oid not in current]
    size = sum(f.size for f in unused)
    print(f"large files stored: {len(stored)}; not used by the current release: {len(unused)}, {size / 1e9:.2f} GB")
    return 0


if __name__ == "__main__":
    sys.exit(main())
