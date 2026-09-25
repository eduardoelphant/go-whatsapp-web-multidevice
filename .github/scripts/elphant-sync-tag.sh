#!/usr/bin/env bash
# Prints the upstream release tag the fork still needs to merge, or nothing.
#
# Usage: elphant-sync-tag.sh <base-ref>
#
# Considers only stable upstream tags (vX.Y.Z; fork tags vX.Y.Z-elphant.N and
# release candidates are ignored) and picks the highest version, so a patch
# for an older line never outranks a newer release. Expects the upstream tags
# to be fetched into this repository.
set -euo pipefail

base_ref=${1:?usage: elphant-sync-tag.sh <base-ref>}

latest=$(git tag -l 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)
if [ -z "$latest" ]; then
	exit 0
fi

if git merge-base --is-ancestor "$latest^{commit}" "$base_ref"; then
	exit 0
fi

printf '%s\n' "$latest"
