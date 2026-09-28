#!/usr/bin/env bash
#
# Removes a stale deb-s3 lock from an apt bucket.
#
# deb-s3 takes a lock object before rewriting a distribution's index files. A
# process that dies between taking the lock and releasing it leaves the lock
# behind, and every later upload to that distribution blocks on it.
#
# The MinaProtocol fork of deb-s3 locks per codename, not per component or
# architecture (lib/deb/s3/lock.rb): it writes dists/<codename>/lockfile.lock,
# then copies it to dists/<codename>/lockfile. Only the second object blocks
# an upload, so it is the one this script looks at. A left-over lockfile.lock
# blocks nothing: the next upload overwrites it.
#
# A young lock is left alone: another upload may be legitimately holding it,
# and removing it would let two writers rewrite the same index at once.
#
# An old lock is deleted only if it is still the object that was examined.
# The delete is conditional on the ETag read before the age check, so a lock
# that another upload took in between is not removed. The ETag of the lock is
# the MD5 of its body, "<user>@<host>", so a new lock with the same body (the
# same user on the same host) is not told apart from the old one.
#
# Exit status: 0 when there is no lock or the stale lock was removed; 1 when
# a lock is still in place (too young, replaced in between, or not readable).
#
# Usage: clear-s3-lock.sh <bucket> <codename>
#
# STALE_AFTER_SECONDS overrides the age threshold, for tests.

set -euo pipefail

BUCKET="${1:?bucket required}"
CODENAME="${2:?codename required}"

# Held longer than this, the owning process is taken to be gone.
STALE_AFTER_SECONDS="${STALE_AFTER_SECONDS:-300}"

KEY="dists/${CODENAME}/lockfile"
LOCK="s3://${BUCKET}/${KEY}"

# ETag and LastModified, tab-separated. A missing object is the normal case
# and is not an error; any other failure is, because it says nothing about
# whether a lock is held.
if ! head="$(aws s3api head-object --bucket "$BUCKET" --key "$KEY" \
               --query '[ETag, LastModified]' --output text 2>&1)"; then
  if echo "$head" | grep -qE '\(404\)|Not Found'; then
    echo "no lock at $LOCK"
    exit 0
  fi
  echo "cannot read $LOCK: $head" >&2
  exit 1
fi

etag="$(echo "$head" | cut -f1)"
held_since="$(echo "$head" | cut -f2)"
age=$(( $(date +%s) - $(date -d "$held_since" +%s) ))

if [ "$age" -le "$STALE_AFTER_SECONDS" ]; then
  echo "lock at $LOCK is ${age}s old; another upload may hold it. Leaving it alone."
  exit 1
fi

echo "lock at $LOCK has been held for ${age}s; removing it (ETag $etag)"
if ! out="$(aws s3api delete-object --bucket "$BUCKET" --key "$KEY" \
              --if-match "$etag" 2>&1)"; then
  if echo "$out" | grep -q 'PreconditionFailed'; then
    echo "lock at $LOCK changed after it was read; another upload holds it now. Leaving it alone."
  elif echo "$out" | grep -q 'NoSuchKey'; then
    # Released by its holder after it was read. That is the outcome wanted.
    echo "lock at $LOCK is already gone"
    exit 0
  else
    echo "cannot remove $LOCK: $out" >&2
  fi
  exit 1
fi
echo "removed $LOCK"
