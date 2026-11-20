#!/usr/bin/env bash
# land.sh: 作業ブランチを main に取り込み、確かめて、PR にする。
#
#   scripts/land.sh merge <branch>...
#       main に各ブランチを --no-ff で取り込み、build / vet / test -race を回す。
#       通ったら .worktrees/<branch> とローカルのブランチを消す。push はしない。
#   scripts/land.sh pr <pr-branch> <title> <body-file>
#       ローカルの main を origin/<pr-branch> として push し、PR を作って CI を待つ。
#       ローカルの main は戻さない (マージ後の git pull で揃う)。
#
# マージコミットの末尾に付ける trailer は、環境変数 LAND_TRAILERS (改行区切り) で渡す。
# 出力は、失敗とまとめだけに絞っている。
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

die() { echo "land: $*" >&2; exit 1; }

LAND_PR_INITIAL_WAIT=${LAND_PR_INITIAL_WAIT:-10}
LAND_PR_RECHECK_WAIT=${LAND_PR_RECHECK_WAIT:-20}

has_no_checks() {
	local out
	out=$(gh pr checks "$1" 2>&1 || true)
	[[ "$out" == *"no checks reported"* ]]
}

check() {
	echo "== build / vet"
	go build ./... && go vet ./...
	echo "== test -race"
	local out
	if ! out=$(go test -race ./... 2>&1); then
		echo "$out" | grep -v '^ok\|no test files'
		die "テストが失敗しました"
	fi
	echo "ok"
}

cmd=${1:-}; shift || true
case "$cmd" in
merge)
	[ $# -ge 1 ] || die "usage: land.sh merge <branch>..."
	[ "$(git branch --show-current)" = main ] || die "main にいません"
	[ -z "$(git status --porcelain --untracked-files=no)" ] || die "main に未コミットの変更があります"
	for b in "$@"; do
		if [ -d ".worktrees/$b" ] && [ -n "$(git -C ".worktrees/$b" status --porcelain)" ]; then
			die ".worktrees/$b に未コミットの変更があります"
		fi
	done
	for b in "$@"; do
		msg="Merge branch '$b'"
		[ -n "${LAND_TRAILERS:-}" ] && msg=$(printf '%s\n\n%s' "$msg" "$LAND_TRAILERS")
		echo "== merge $b ($(git rev-list --count "main..$b") commits)"
		git merge --no-ff -q "$b" -m "$msg" || die "$b の取り込みでぶつかりました。解決してから再実行してください"
	done
	check
	for b in "$@"; do
		[ -d ".worktrees/$b" ] && git worktree remove ".worktrees/$b"
		# -d は upstream (origin/main) に入っているかで判定し、push 前は失敗する。
		# main に取り込めたことを確かめてから -D で消す。
		git merge-base --is-ancestor "$b" HEAD || die "$b が main に入っていません"
		git branch -D -q "$b"
	done
	git worktree prune
	echo "== main は origin より $(git rev-list --count origin/main..main) コミット先。push はしていません"
	;;
pr)
	[ $# -eq 3 ] || die "usage: land.sh pr <pr-branch> <title> <body-file>"
	[ "$(git branch --show-current)" = main ] || die "main にいません"
	git push -q origin "main:refs/heads/$1"
	url=$(gh pr create --base main --head "$1" --title "$2" --body-file "$3")
	echo "$url"
	sleep "$LAND_PR_INITIAL_WAIT"
	# 文書だけの PR では CI が回らない (ci.yml の paths)。そのときは待たない。
	# 作った直後はまだ登録されていないことがあるので、少し待って確かめ直す。
	if has_no_checks "$url" && sleep "$LAND_PR_RECHECK_WAIT" && has_no_checks "$url"; then
		echo "CI: none (文書だけの変更)"
	else
		if gh pr checks "$url" --watch --fail-fast >/dev/null 2>&1; then
			echo "CI: pass"
		else
			summary=$(gh pr checks "$url" 2>&1 || true)
			printf '%s\n' "$summary" | grep -v 'pass' || true
			die "CI が失敗しました"
		fi
	fi
	;;
*)
	die "usage: land.sh merge <branch>... | pr <pr-branch> <title> <body-file>"
	;;
esac
