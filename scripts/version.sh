#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)

head_tag=$(git -C "$project_root" tag --points-at HEAD --sort=-version:refname | awk '/^v0\.1\.[0-9]+$/ { print; exit }')
if [ -n "$head_tag" ]; then
  printf '%s\n' "${head_tag#v}"
  exit 0
fi

latest_tag=$(git -C "$project_root" tag --list 'v0.1.*' --sort=-version:refname | awk '/^v0\.1\.[0-9]+$/ { print; exit }')
if [ -z "$latest_tag" ]; then
  printf '0.1.0\n'
  exit 0
fi

latest_patch=${latest_tag##*.}
printf '0.1.%s\n' "$((latest_patch + 1))"
