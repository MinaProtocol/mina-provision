#!/usr/bin/env bash
#
# Runs govulncheck against the module and fails when the code reaches a known
# vulnerability.
#
# govulncheck reports two kinds of finding: vulnerabilities the code can reach
# through its call graph, and vulnerabilities in packages or modules it only
# imports. Only the first kind makes the exit status non-zero.
#
# The version is pinned so that a new govulncheck release cannot change the
# result of an unchanged commit. Dependabot does not update it; bump it by hand.
#
# Usage: vulncheck.sh [packages...]   (default: ./...)

set -euo pipefail

GOVULNCHECK_VERSION="v1.8.0"

if [ "$#" -eq 0 ]; then
  set -- ./...
fi

go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" "$@"
