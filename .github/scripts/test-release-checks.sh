#!/usr/bin/env bash
#
# Offline tests for the checks the Release workflow runs: verify-listed.sh
# and validate-tag.sh. They run the same scripts the workflow runs.
#
# testdata/deb-s3-list-stable-noble-amd64.txt is a real `deb-s3 list` output
# of stable.apt.packages.minaprotocol.com, codename noble, component stable,
# arch amd64. It holds the rows of other Mina packages next to the
# mina-provision rows, which is what makes a loose match pass by mistake.
#
# Usage: test-release-checks.sh

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
FIXTURE="$HERE/testdata/deb-s3-list-stable-noble-amd64.txt"

failed=0
check() { # <description> <expected status> <actual status>
  if [ "$2" -eq "$3" ]; then
    echo "ok    $1"
  else
    echo "FAIL  $1: expected exit $2, got $3"
    failed=1
  fi
}

listed() { # <package> <version> <arch>, listing from the fixture
  "$HERE/verify-listed.sh" "$@" < "$FIXTURE" > /dev/null
}

echo "--- verify-listed.sh"

listed mina-provision 0.0.3 amd64;          check "published version is found"                  0 $?
listed mina-provision 0.0.2 amd64;          check "older published version is found"            0 $?
listed mina-provision 0.0.4 amd64;          check "unpublished version is not found"            1 $?
listed mina-provision 0.0.3 arm64;          check "published version, other arch, not found"    1 $?
listed mina-provision 0.0 amd64;            check "version prefix is not a match"               1 $?

# The case from issue #14. The old check, grep for the version, passes here
# on mina-archive 3.3.0-8c0c2e6: the dots match any character.
grep -q "0.8.0" "$FIXTURE";                 check "fixture has the grep false match for 0.8.0"  0 $?
listed mina-provision 0.8.0 amd64;          check "0.8.0 is not found"                          1 $?
listed mina-provision 3.3.0 amd64;          check "other package's version prefix not found"    1 $?
listed mina-provision 3.3.0-8c0c2e6 amd64;  check "other package's exact version not found"     1 $?
listed mina-archive 3.3.0-8c0c2e6 amd64;    check "exact match works for any package"           0 $?

"$HERE/verify-listed.sh" mina-provision 0.0.3 amd64 < /dev/null > /dev/null
check "empty listing is not found" 1 $?
"$HERE/verify-listed.sh" mina-provision 0.0.3 < "$FIXTURE" > /dev/null 2>&1
check "missing argument is a usage error" 2 $?

echo "--- validate-tag.sh"

# A throw-away repository with its own "origin", so the ancestry check runs
# without the network.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
(
  set -e
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid
  export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
  # Not `git init -b`: it needs git 2.28 or later.
  git init -q --bare "$work/origin.git"
  git -C "$work/origin.git" symbolic-ref HEAD refs/heads/main
  git clone -q "$work/origin.git" "$work/clone" 2> /dev/null
  cd "$work/clone"
  git symbolic-ref HEAD refs/heads/main
  git commit -q --allow-empty -m on-main
  git push -q origin main
  git checkout -q -b side
  git commit -q --allow-empty -m off-main
  git tag -a -m annotated v9.9.9 main
) || { echo "FAIL  could not set up the test repository"; exit 1; }
on_main="$(git -C "$work/clone" rev-parse main)"
off_main="$(git -C "$work/clone" rev-parse side)"

tag() { # <tag> <commit>
  (cd "$work/clone" && "$HERE/validate-tag.sh" "$@") > /dev/null 2>&1
}

tag v1.2.3 "$on_main";         check "vX.Y.Z on main is accepted"          0 $?
tag v10.20.30 "$on_main";      check "multi-digit vX.Y.Z is accepted"      0 $?
tag v9.9.9 v9.9.9;             check "annotated tag object is peeled"      0 $?
tag v1.2.3 "$off_main";        check "commit not on main is refused"       1 $?
tag v1.0.0-rc1 "$on_main";     check "pre-release suffix is refused"       1 $?
tag 1.0.0 "$on_main";          check "missing v is refused"                1 $?
tag v1.0 "$on_main";           check "two components are refused"          1 $?
tag v1.0.0.1 "$on_main";       check "four components are refused"         1 $?
tag "v1.0.0 " "$on_main";      check "trailing space is refused"           1 $?
tag v1.2.3;                    check "missing argument is a usage error"   2 $?

exit "$failed"
