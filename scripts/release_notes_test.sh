#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
test_repository=$(mktemp -d)
trap 'rm -rf "$test_repository"' EXIT
mkdir -p "$test_repository/scripts" "$test_repository/bin" "$test_repository/packages"
cp "$project_root/scripts/release_notes.sh" "$test_repository/scripts/release_notes.sh"
cd "$test_repository"
git init -q -b main
git config user.email plateau-release-test@example.invalid
git config user.name plateau-release-test
printf '# 설치·사용 안내\n' >README.md
printf '# 개발·검증 안내\n' >CONTRIBUTING.md
git add README.md CONTRIBUTING.md
git commit -qm '초기 데스크톱 제공'
first_commit=$(git rev-parse HEAD)
git tag v0.1.0
git commit --allow-empty -qm '백업 오류 수정'
second_commit=$(git rev-parse HEAD)
git tag v0.1.1
git commit --allow-empty -qm '다음 버전에서만 제공'

# 서명 검사만 대체하고 커밋 범위와 체크섬은 실제 도구로 확인합니다.
cat >bin/pkgutil <<'SH'
#!/bin/sh
case "${TEST_SIGNATURE:-unsigned}" in
  unsigned) printf 'Status: no signature\n'; exit 1 ;;
  signed) printf 'Status: signed by a certificate trusted by Mac OS X\n' ;;
  *) printf 'Could not open package\n' >&2; exit 1 ;;
esac
SH
chmod +x bin/pkgutil
PATH="$test_repository/bin:$PATH"
export PATH
for version in 0.1.0 0.1.1; do
  printf '패키지 %s\n' "$version" >"packages/plateau-${version}-macos-universal.pkg"
  (cd packages && shasum -a 256 "plateau-${version}-macos-universal.pkg" >"plateau-${version}-macos-universal.pkg.sha256")
done

sh scripts/release_notes.sh 0.1.0 "$first_commit" packages/plateau-0.1.0-macos-universal.pkg >first.md
grep -q '초기 데스크톱 제공' first.md
! grep -q '백업 오류 수정\|다음 버전에서만 제공' first.md
grep -q '서명되지 않은 패키지' first.md
grep -Fq "대상 커밋: \`$first_commit\`" first.md

TEST_SIGNATURE=signed sh scripts/release_notes.sh 0.1.1 "$second_commit" packages/plateau-0.1.1-macos-universal.pkg >second.md
grep -q '백업 오류 수정' second.md
! grep -q '초기 데스크톱 제공\|다음 버전에서만 제공' second.md
grep -q '서명된 패키지' second.md
grep -Fq 'compare/v0.1.0...v0.1.1' second.md
package_hash=$(shasum -a 256 packages/plateau-0.1.1-macos-universal.pkg | cut -d ' ' -f 1)
grep -Fq "$package_hash" second.md

# 문서 링크는 릴리스 태그의 실제 파일을 가리켜야 합니다.
for notes in first.md second.md; do
  document_refs=$(grep -Eo 'https://github.com/creatrip/plateau/(blob|tree)/v0\.1\.[0-9]+/[^)]+' "$notes" | sed -E 's#https://github.com/creatrip/plateau/(blob|tree)/([^/]+)/#\2:#')
  test -n "$document_refs"
  printf '%s\n' "$document_refs" | while IFS= read -r document_ref; do
    git cat-file -e "$document_ref"
  done
done

if sh scripts/release_notes.sh 0.1.0 "$second_commit" packages/plateau-0.1.0-macos-universal.pkg >invalid.md 2>/dev/null; then
  printf '대상 커밋과 태그 불일치를 거부하지 않았습니다.\n' >&2; exit 1
fi
if TEST_SIGNATURE=invalid sh scripts/release_notes.sh 0.1.1 "$second_commit" packages/plateau-0.1.1-macos-universal.pkg >invalid.md 2>/dev/null; then
  printf '서명 검사 실패를 거부하지 않았습니다.\n' >&2; exit 1
fi
printf '변조\n' >>packages/plateau-0.1.1-macos-universal.pkg
if sh scripts/release_notes.sh 0.1.1 "$second_commit" packages/plateau-0.1.1-macos-universal.pkg >invalid.md 2>/dev/null; then
  printf '체크섬 불일치를 거부하지 않았습니다.\n' >&2; exit 1
fi
test ! -s invalid.md
printf '릴리스 설명의 커밋 범위, 서명 상태와 체크섬 검사를 통과했습니다.\n'

# 실제 게시 작업을 로컬 Git 원격과 가짜 GitHub 명령으로 실행합니다.
cp "$project_root/scripts/version.sh" scripts/version.sh
awk '/^\[tasks.release\]$/ { task=1; next } task && /^run = / { body=1; next } body && /^\x27\x27\x27$/ { exit } body { print }' "$project_root/mise.toml" >release.sh
test -s release.sh
printf '*\n' >.git/info/exclude
git init -q --bare origin.git
git remote add origin "$test_repository/origin.git"
git push -q origin main refs/tags/v0.1.0 refs/tags/v0.1.1
mkdir -p dist
printf '게시 패키지\n' >dist/plateau-0.1.2-macos-universal.pkg
(cd dist && shasum -a 256 plateau-0.1.2-macos-universal.pkg >plateau-0.1.2-macos-universal.pkg.sha256)
cat >bin/mise <<'SH'
#!/bin/sh
set -eu
test "$1" = run
case "$2" in check|package-test) printf '%s\n' "$2" >>checks.log ;; *) exit 1 ;; esac
SH
cat >bin/gh <<'SH'
#!/bin/sh
set -eu
case "$1 $2" in
  'auth status') exit 0 ;;
  'release view') test -f published; exit $? ;;
  'release create')
    test "$3" = v0.1.2
    test -f "$4"
    test -f "$5"
    shift 5
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --title) printf '%s\n' "$2" >published-title; shift 2 ;;
        --notes-file) cp "$2" published-notes; shift 2 ;;
        --verify-tag|--latest) shift ;;
        *) printf '예상하지 않은 게시 인자: %s\n' "$1" >&2; exit 1 ;;
      esac
    done
    test -s published-title
    test -s published-notes
    touch published
    ;;
  *) exit 1 ;;
esac
SH
chmod +x bin/mise bin/gh
printf '변조\n' >>dist/plateau-0.1.2-macos-universal.pkg
if sh release.sh >failed-release.log 2>&1; then
  printf '설명 생성 실패 후 게시 절차를 중단하지 않았습니다.\n' >&2; exit 1
fi
test ! -f published
test -z "$(git tag --list v0.1.2)"
rm checks.log
(cd dist && shasum -a 256 plateau-0.1.2-macos-universal.pkg >plateau-0.1.2-macos-universal.pkg.sha256)
sh -eu release.sh
test "$(cat published-title)" = 'v0.1.2'
sh scripts/release_notes.sh 0.1.2 HEAD dist/plateau-0.1.2-macos-universal.pkg >expected-notes
cmp expected-notes published-notes
test "$(git --git-dir=origin.git rev-parse 'v0.1.2^{}')" = "$(git rev-parse HEAD)"
test "$(cat checks.log)" = "$(printf 'check\npackage-test')"
sh -eu release.sh
test "$(cat checks.log)" = "$(printf 'check\npackage-test')"
cmp expected-notes published-notes
printf '게시 제목·본문 일치와 기존 릴리스 재실행 검사를 통과했습니다.\n'
