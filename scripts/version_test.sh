#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
test_repository=$(mktemp -d)
trap 'rm -rf "$test_repository"' EXIT

mkdir -p "$test_repository/scripts"
cp "$project_root/scripts/version.sh" "$test_repository/scripts/version.sh"
cd "$test_repository"
git init -q
git config user.email plateau-version-test@example.invalid
git config user.name plateau-version-test
printf 'first\n' >state
git add state scripts/version.sh
git commit -qm first

test "$(sh scripts/version.sh)" = "0.1.0"
git tag v0.1.0
test "$(sh scripts/version.sh)" = "0.1.0"

printf 'second\n' >state
git add state
git commit -qm second
test "$(sh scripts/version.sh)" = "0.1.1"
git tag v0.2.99
git tag v0.1.invalid
test "$(sh scripts/version.sh)" = "0.1.1"

git tag v0.1.1
test "$(sh scripts/version.sh)" = "0.1.1"
printf 'third\n' >state
git add state
git commit -qm third
test "$(sh scripts/version.sh)" = "0.1.2"
