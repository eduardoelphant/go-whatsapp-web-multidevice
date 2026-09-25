#!/usr/bin/env bash
# Tests for elphant-sync-tag.sh. Run: bash .github/scripts/elphant-sync-tag_test.sh
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/elphant-sync-tag.sh"
failures=0
repo=""

new_repo() {
	repo=$(mktemp -d)
	git -C "$repo" init -q -b main
	git -C "$repo" config user.email test@example.com
	git -C "$repo" config user.name test
	git -C "$repo" config commit.gpgsign false
	git -C "$repo" config tag.gpgsign false
}

commit() {
	git -C "$repo" commit -q --allow-empty -m "$1"
}

check() {
	local name=$1 base=$2 want=$3 got
	got=$(cd "$repo" && bash "$script" "$base")
	if [ "$got" = "$want" ]; then
		echo "ok   $name"
	else
		echo "FAIL $name: got '$got', want '$want'"
		failures=$((failures + 1))
	fi
}

# Upstream released v1.2.0 and v1.10.0; the fork branched at v1.2.0.
new_repo
commit "upstream 1.2.0"
git -C "$repo" tag v1.2.0
git -C "$repo" branch fork
commit "upstream 1.10.0"
git -C "$repo" tag v1.10.0
commit "upstream 2.0.0 release candidate"
git -C "$repo" tag v2.0.0-rc1
git -C "$repo" checkout -q fork
commit "fork change"
git -C "$repo" tag v1.2.0-elphant.1

check "picks the highest stable tag the base lacks" fork v1.10.0

# Upstream patches the old line after the newer release.
git -C "$repo" checkout -q v1.2.0
commit "upstream 1.2.1"
git -C "$repo" tag v1.2.1
git -C "$repo" checkout -q fork
check "older patch tag after newer release" fork v1.10.0

git -C "$repo" merge -q --no-edit v1.10.0
check "nothing to do once the base contains it" fork ""
rm -rf "$repo"

new_repo
commit "initial"
check "nothing to do without tags" main ""
rm -rf "$repo"

if [ "$failures" -ne 0 ]; then
	echo "$failures failure(s)"
	exit 1
fi
