#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  printf '사용법: mise run release-notes -- <0.1.x> <커밋 또는 태그> <패키지 경로>\n' >&2
  exit 1
fi
plateau_version=$1
target=$2
package_path=$3
if ! printf '%s\n' "$plateau_version" | grep -Eq '^0\.1\.[0-9]+$'; then
  printf '릴리스 버전은 0.1.x 형식이어야 합니다.\n' >&2
  exit 1
fi
project_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
head_commit=$(git -C "$project_root" rev-parse --verify "${target}^{commit}")
release_tag="v${plateau_version}"
tag_commit=$(git -C "$project_root" rev-parse --verify "refs/tags/${release_tag}^{commit}" 2>/dev/null || true)
if [ -n "$tag_commit" ] && [ "$tag_commit" != "$head_commit" ]; then
  printf '릴리스 태그가 대상 커밋과 다릅니다.\n' >&2
  exit 1
fi

package_name="plateau-${plateau_version}-macos-universal.pkg"
if [ "$(basename "$package_path")" != "$package_name" ]; then
  printf '패키지 파일명과 릴리스 버전이 다릅니다.\n' >&2
  exit 1
fi
package_hash=$(shasum -a 256 "$package_path" | cut -d ' ' -f 1)
if [ "$(cat "${package_path}.sha256")" != "$package_hash  $package_name" ]; then
  printf '패키지와 체크섬 파일이 일치하지 않습니다.\n' >&2
  exit 1
fi
if signature_result=$(LC_ALL=C pkgutil --check-signature "$package_path" 2>&1); then
  signature_note='서명된 패키지입니다. 공증은 제공하지 않습니다.'
else
  case "$signature_result" in
    *'Status: no signature'*) signature_note='서명되지 않은 패키지입니다. 공증은 제공하지 않습니다.' ;;
    *) printf '패키지 서명 상태를 확인할 수 없습니다.\n%s\n' "$signature_result" >&2; exit 1 ;;
  esac
fi

previous_tag=$(git -C "$project_root" tag --merged "$head_commit" --sort=-version:refname | awk -v patch="${plateau_version##*.}" '/^v0\.1\.[0-9]+$/ { split($0, parts, "."); if (parts[3] + 0 < patch + 0) { print; exit } }')
commit_range=$head_commit
if [ -n "$previous_tag" ]; then
  commit_range="${previous_tag}..${head_commit}"
fi
repository_url=https://github.com/creatrip/plateau
changes=$(git -C "$project_root" log --reverse --format="- %s ([%h](${repository_url}/commit/%H))" "$commit_range")

cat <<EOF
## 변경 사항

$changes
EOF
if [ -n "$previous_tag" ]; then
  printf '\n[이전 버전과 비교](%s/compare/%s...%s)\n' "$repository_url" "$previous_tag" "$release_tag"
fi
cat <<EOF

## 배포 정보

대상 커밋: \`$head_commit\`
패키지 SHA-256: \`$package_hash\`

$signature_note

[설치·사용 안내](${repository_url}/blob/${release_tag}/README.md) · [개발·검증 안내](${repository_url}/blob/${release_tag}/CONTRIBUTING.md)
EOF
