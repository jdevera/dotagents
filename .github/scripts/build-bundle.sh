#!/usr/bin/env bash
# Build the release artifacts for a given version, in the current
# working directory:
#   dotagents-<version>.tar.gz  — full ~/.agents snapshot to extract at $HOME
#   skill-lock-<version>.json   — copy of the lockfile, for inspection
#
# The tarball contains the repo's tracked content (excluding .git and
# .github) plus every skill resolved from .skill-lock.json's recorded
# sources, all rooted under .agents/.
#
# Usage: build-bundle.sh <version>
set -euo pipefail

VERSION="${1:?usage: $0 <version>}"
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)

staging=$(mktemp -d)/.agents
mkdir -p "$staging"

# Repo content (excluding .git/.github).
rsync -a --exclude='.git' --exclude='.github' "$REPO_ROOT/" "$staging/"

# Skills, resolved from .skill-lock.json sources.
mkdir -p "$staging/skills"
jq -c '.skills | to_entries[]' "$REPO_ROOT/.skill-lock.json" | while read -r entry; do
  name=$(jq -r '.key'                <<<"$entry")
  url=$(jq -r '.value.sourceUrl'     <<<"$entry")
  ref=$(jq -r '.value.ref // "main"' <<<"$entry")
  skill_path=$(jq -r '.value.skillPath // empty' <<<"$entry")

  echo "::group::$name ($url @ $ref)"
  clone=$(mktemp -d)
  git clone --quiet --depth 1 --branch "$ref" "$url" "$clone" \
    || git clone --quiet "$url" "$clone"

  if [[ -n "$skill_path" ]]; then
    src="$clone/$(dirname "$skill_path")"
  else
    src="$clone"
  fi

  mkdir -p "$staging/skills/$name"
  rsync -a \
    --exclude='.git' \
    --exclude='node_modules' \
    --exclude='.DS_Store' \
    "$src/" "$staging/skills/$name/"
  echo "::endgroup::"
done

archive="dotagents-${VERSION}.tar.gz"
manifest="skill-lock-${VERSION}.json"
tar czf "$archive" -C "$(dirname "$staging")" .agents
cp "$REPO_ROOT/.skill-lock.json" "$manifest"

echo "$archive"
echo "$manifest"
