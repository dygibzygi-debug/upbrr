#!/usr/bin/env bash
# Copyright (c) 2026, Zulea contributors.
# SPDX-License-Identifier: GPL-2.0-or-later
#
# Merge upstream upbrr into a NEW local branch and test the Zulea uploader
# against it. Never pushes, never touches the current branch.
#
# Usage: scripts/zulea/upstream-sync.sh [upstream-url] [upstream-branch]
set -euo pipefail

UPSTREAM_URL="${1:-https://github.com/autobrr/upbrr}"
UPSTREAM_BRANCH="${2:-main}"
UPSTREAM_REF="refs/remotes/upstream/${UPSTREAM_BRANCH}"
SYNC_BRANCH="zulea/upstream-sync-$(date -u +%Y%m%d-%H%M%S)"

# Areas the uploader depends on. Changes here need a manual review.
DEPENDENCIES=(
	internal/trackers/impl/unit3d
	internal/trackers/dupe
	internal/trackers/data
	internal/trackers/submission
	internal/metadata/mediainfo
	internal/pathing
	internal/redaction
	pkg/api
)

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

if [ -n "$(git status --porcelain)" ]; then
	echo "STOP: the working tree is not clean; commit or stash first." >&2
	exit 2
fi

START_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
echo "== fetch ${UPSTREAM_URL} ${UPSTREAM_BRANCH}"
git fetch --no-tags "$UPSTREAM_URL" "+refs/heads/${UPSTREAM_BRANCH}:${UPSTREAM_REF}"

BASE="$(git merge-base HEAD "$UPSTREAM_REF")"
NEW="$(git rev-list --count "${BASE}..${UPSTREAM_REF}")"
echo "== upstream commits not yet merged: ${NEW}"
if [ "$NEW" = "0" ]; then
	echo "Nothing to merge."
	exit 0
fi

echo "== changes in dependency areas (${BASE:0:12}..upstream)"
git diff --stat "$BASE" "$UPSTREAM_REF" -- "${DEPENDENCIES[@]}" 'internal/trackers/*.go' || true

git switch -c "$SYNC_BRANCH"
if ! git merge --no-edit "$UPSTREAM_REF"; then
	echo "STOP: merge conflict. Conflicting files:" >&2
	git diff --name-only --diff-filter=U >&2 || true
	git merge --abort
	git switch "$START_BRANCH"
	git branch -D "$SYNC_BRANCH"
	exit 3
fi

status=0
echo "== build"
go build -o /dev/null ./cmd/zulea-upbrr/ || status=1
echo "== tests (including the PTT contract test)"
go test -race ./internal/zulea/... ./cmd/zulea-upbrr/ \
	./internal/trackers/impl/unit3d/... ./internal/trackers/dupe/... \
	./internal/metadata/mediainfo/... || status=1

echo
if [ "$status" -ne 0 ]; then
	echo "RESULT: FAILED on ${SYNC_BRANCH}. Do not deploy. Review the failures above."
else
	echo "RESULT: OK on ${SYNC_BRANCH}."
fi
cat <<EOF
Next steps (manual):
  1. Review the dependency diff above and: git log --oneline ${BASE:0:12}..${UPSTREAM_REF}
  2. If accepted, merge ${SYNC_BRANCH} into the uploader branch and push it yourself.
  3. Rebuild the image and run check-config and check before any upload.
  To discard: git switch ${START_BRANCH} && git branch -D ${SYNC_BRANCH}
This script never pushes.
EOF
exit "$status"
