#!/usr/bin/env bash
#
# Refuses a release tag that is not a plain vX.Y.Z or that does not point at a
# commit on main.
#
# The release workflows fire on any `v*` tag and publish to the stable
# repository. Without this check `v1.0.0-rc1` would be signed and published
# as stable, and a tag on an unreviewed branch commit would publish code that
# never passed the branch protection of main.
#
# The format is checked first and needs no git access. The ancestry check
# needs the full history (checkout with fetch-depth: 0).
#
# Usage: validate-tag.sh <tag> <commit>
#
# Exit status: 0 when the tag is valid, 1 when it is not, 2 on a usage error.

set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: validate-tag.sh <tag> <commit>" >&2
  exit 2
fi

TAG="$1"
COMMIT="$2"

if ! [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "tag must be vX.Y.Z, got $TAG" >&2
  exit 1
fi

# Fetched again, not taken from the checkout: the tracking ref must show
# main as it is now. The refspec is explicit so that the result does not
# depend on how the checkout configured the remote.
git fetch --quiet --no-tags origin +refs/heads/main:refs/remotes/origin/main
# ^{commit} peels an annotated tag to the commit it points at.
if ! git merge-base --is-ancestor "${COMMIT}^{commit}" origin/main; then
  echo "tag $TAG points at $COMMIT, which is not on main" >&2
  exit 1
fi

echo "tag $TAG is valid and on main"
