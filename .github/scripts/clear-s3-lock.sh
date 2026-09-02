#!/usr/bin/env bash
#
# Removes a stale deb-s3 lock from an apt bucket.
#
# deb-s3 takes a lock object before rewriting a distribution's index files. A
# process that dies between taking the lock and releasing it leaves the lock
# behind, and every later upload to that distribution blocks on it.
#
# A young lock is left alone: another upload may be legitimately holding it,
# and removing it would let two writers rewrite the same index at once.
#
# Usage: clear-s3-lock.sh <bucket> <codename> <component>

set -euo pipefail

BUCKET="${1:?bucket required}"
CODENAME="${2:?codename required}"
COMPONENT="${3:?component required}"

# Held longer than this, the owning process is taken to be gone.
STALE_AFTER_SECONDS=300

LOCK="s3://${BUCKET}/dists/${CODENAME}/${COMPONENT}/binary-/lockfile"

listing="$(aws s3 ls "$LOCK" 2>/dev/null || true)"
if [ -z "$listing" ]; then
  echo "no lock at $LOCK"
  exit 0
fi

held_since="$(echo "$listing" | awk '{print $1 " " $2}')"
age=$(( $(date +%s) - $(date -d "$held_since" +%s) ))

if [ "$age" -le "$STALE_AFTER_SECONDS" ]; then
  echo "lock at $LOCK is ${age}s old; another upload may hold it. Leaving it alone."
  exit 1
fi

echo "lock at $LOCK has been held for ${age}s; removing it"
aws s3 rm "$LOCK"
