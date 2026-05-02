#!/usr/bin/env bash
# Print the next release version: YYYY-MM-DD.N
#
# N counts how many earlier commits (on the same calendar day as HEAD)
# touched .skill-lock.json. The first lockfile-touching commit of the
# day is .0, the second .1, and so on.
#
# Usage: compute-version.sh
set -euo pipefail

today=$(git show -s --format=%cs HEAD)
count=$(git log --pretty=format:'%cs' HEAD -- .skill-lock.json | grep -c "^$today$" || true)
echo "$today.$((count - 1))"
