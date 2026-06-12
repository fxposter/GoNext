#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/.." && pwd)"
cd "$ROOT"

run_case() {
	local name="$1"
	local expected="$2"
	local actual

	actual="$(./gonext run "tests/$name.gn")"
	if [[ "$actual" != "$expected" ]]; then
		echo "test failed: $name" >&2
		echo "expected:" >&2
		printf '%s\n' "$expected" >&2
		echo "actual:" >&2
		printf '%s\n' "$actual" >&2
		exit 1
	fi
	echo "ok: $name"
}

run_fail_case() {
	local name="$1"
	local expected_error="$2"
	local stderr_file
	stderr_file="$(mktemp)"

	if ./gonext run "tests/$name.gn" 2>"$stderr_file"; then
		echo "test failed: $name (expected an error)" >&2
		rm -f "$stderr_file"
		exit 1
	fi
	if ! grep -q "$expected_error" "$stderr_file"; then
		echo "test failed: $name" >&2
		echo "expected error containing: $expected_error" >&2
		echo "actual:" >&2
		cat "$stderr_file" >&2
		rm -f "$stderr_file"
		exit 1
	fi
	rm -f "$stderr_file"
	echo "ok: $name"
}

run_case "default_named" $'1 2 3 10\n3 4 9 10'
run_case "extension_lambda" '4,16'
run_case "enum_match" $'ok 5\n5\nerr boom\nempty'
run_case "match_exhaustive" '12 100 9 0'
run_case "enum_multi" $'text:hello\ntext:world\nspace\ntrue false'
run_case "lambda_context" '[2 4 6]'
run_case "lambda_assign" $'42\n8'
run_case "named_imported" 'ababab'
run_case "extension_import" $'HELLO WORLD\n3 a'
run_case "defaults_expr" '209 205 310'
run_case "combined" $'42 7\nHello, world\nHi, light\n[N, 1 N, 2 N, 3]\nnone'
run_fail_case "fail_nonexhaustive" 'non-exhaustive match'
