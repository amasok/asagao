#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "$0")" && pwd)
land_sh="$script_dir/land.sh"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

git init --bare -q "$tmpdir/remote.git"
git init -q "$tmpdir/repo"
git -C "$tmpdir/repo" checkout -q -b main
git -C "$tmpdir/repo" config user.name "Test User"
git -C "$tmpdir/repo" config user.email "test@example.com"

mkdir -p "$tmpdir/repo/scripts"
cp "$land_sh" "$tmpdir/repo/scripts/land.sh"
chmod +x "$tmpdir/repo/scripts/land.sh"
printf 'init\n' > "$tmpdir/repo/README.md"
git -C "$tmpdir/repo" add README.md scripts/land.sh
git -C "$tmpdir/repo" commit -q -m "init"
git -C "$tmpdir/repo" remote add origin "$tmpdir/remote.git"
git -C "$tmpdir/repo" push -q -u origin main

printf 'PR body\n' > "$tmpdir/body.md"

mkdir -p "$tmpdir/bin"
cat > "$tmpdir/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "pr" ] && [ "${2:-}" = "create" ]; then
	echo "https://example.com/pr/1"
	exit 0
fi

if [ "${1:-}" = "pr" ] && [ "${2:-}" = "checks" ]; then
	watch=0
	for arg in "$@"; do
		if [ "$arg" = "--watch" ]; then
			watch=1
		fi
	done

	case "${MOCK_GH_CASE:-}" in
	doc_only)
		if [ "$watch" -eq 1 ]; then
			echo "unexpected --watch call in doc_only" >&2
			exit 1
		fi
		echo "no checks reported on the 'doc-branch' branch" >&2
		exit 1
		;;
	ci_pass)
		if [ "$watch" -eq 1 ]; then
			exit 0
		fi
		printf 'lint\tpass\t5s\thttps://example.com/1\n'
		printf 'test\tpending\t0s\thttps://example.com/2\n'
		exit 0
		;;
	ci_fail)
		if [ "$watch" -eq 1 ]; then
			exit 1
		fi
		printf 'lint\tpass\t5s\thttps://example.com/1\n'
		printf 'test\tfail\t12s\thttps://example.com/2\n'
		exit 1
		;;
	delayed_check)
		if [ "$watch" -eq 1 ]; then
			exit 0
		fi
		count_file="$MOCK_GH_STATE_DIR/count"
		count=0
		if [ -f "$count_file" ]; then
			count=$(cat "$count_file")
		fi
		count=$((count + 1))
		echo "$count" > "$count_file"
		if [ "$count" -eq 1 ]; then
			echo "no checks reported on the 'delayed-branch' branch" >&2
			exit 1
		fi
		printf 'test\tpending\t0s\thttps://example.com/3\n'
		exit 0
		;;
	*)
		echo "unknown MOCK_GH_CASE: ${MOCK_GH_CASE:-}" >&2
		exit 2
		;;
	esac
fi

echo "unexpected gh command: $*" >&2
exit 2
EOF
chmod +x "$tmpdir/bin/gh"

fail() {
	printf 'FAIL: %b\n' "$*" >&2
	exit 1
}

assert_contains() {
	local haystack="$1" needle="$2" label="$3"
	if [[ "$haystack" != *"$needle"* ]]; then
		fail "$label: expected output to contain '$needle', got:\n$haystack"
	fi
}

assert_not_contains() {
	local haystack="$1" needle="$2" label="$3"
	if [[ "$haystack" == *"$needle"* ]]; then
		fail "$label: expected output NOT to contain '$needle', got:\n$haystack"
	fi
}

run_case() {
	local case_name="$1" branch="$2"
	local state_dir="$tmpdir/state-$case_name"
	mkdir -p "$state_dir"
	local out_file="$tmpdir/out-$case_name.log"
	local status=0
	(
		cd "$tmpdir/repo"
		PATH="$tmpdir/bin:$PATH" \
		MOCK_GH_CASE="$case_name" \
		MOCK_GH_STATE_DIR="$state_dir" \
		LAND_PR_INITIAL_WAIT=0 \
		LAND_PR_RECHECK_WAIT=0 \
		bash "$tmpdir/repo/scripts/land.sh" pr "$branch" "Test PR ($case_name)" "$tmpdir/body.md"
	) >"$out_file" 2>&1 || status=$?
	RUN_STATUS=$status
	RUN_OUTPUT=$(cat "$out_file")
}

# Case 1: 文書のみ PR
run_case "doc_only" "doc-branch"
[ "$RUN_STATUS" -eq 0 ] || fail "Case 1 (doc_only): expected exit 0, got $RUN_STATUS\n$RUN_OUTPUT"
assert_contains "$RUN_OUTPUT" "CI: none (文書だけの変更)" "Case 1 (doc_only)"

# Case 2: 通常 PR で CI 成功
run_case "ci_pass" "pass-branch"
[ "$RUN_STATUS" -eq 0 ] || fail "Case 2 (ci_pass): expected exit 0, got $RUN_STATUS\n$RUN_OUTPUT"
assert_contains "$RUN_OUTPUT" "CI: pass" "Case 2 (ci_pass)"

# Case 3: CI 失敗
run_case "ci_fail" "fail-branch"
[ "$RUN_STATUS" -eq 1 ] || fail "Case 3 (ci_fail): expected exit 1, got $RUN_STATUS\n$RUN_OUTPUT"
assert_contains "$RUN_OUTPUT" "test	fail" "Case 3 (ci_fail)"
assert_contains "$RUN_OUTPUT" "land: CI が失敗しました" "Case 3 (ci_fail)"
assert_not_contains "$RUN_OUTPUT" "CI: pass" "Case 3 (ci_fail)"

# Case 4: チェック登録遅延
run_case "delayed_check" "delayed-branch"
[ "$RUN_STATUS" -eq 0 ] || fail "Case 4 (delayed_check): expected exit 0, got $RUN_STATUS\n$RUN_OUTPUT"
assert_contains "$RUN_OUTPUT" "CI: pass" "Case 4 (delayed_check)"

echo "ok: all 4 cases passed"
