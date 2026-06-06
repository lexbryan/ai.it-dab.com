#!/usr/bin/env bash
# coverage-gate.sh PROFILE MIN
#
# Fail (exit 1) when total backend statement coverage is below MIN percent.
# Generated/boilerplate that is not meaningfully unit-testable is excluded from
# the denominator: the embedded SQL migrations (internal/db handles them) and the
# cmd/* entrypoint mains (thin CLI/signal plumbing). The same command runs locally
# (`make cover`) and in CI, so the number is reproducible.
set -euo pipefail

profile="${1:-coverage.out}"
min="${2:-80}"

if [ ! -f "$profile" ]; then
	echo "coverage-gate: profile not found: $profile" >&2
	exit 2
fi

filtered="$(mktemp)"
trap 'rm -f "$filtered"' EXIT
# Keep the "mode:" header line; drop migration and cmd-main coverage lines.
grep -vE '/(migrations|cmd)/' "$profile" >"$filtered"

total="$(go tool cover -func="$filtered" | awk '/^total:/ {print substr($3, 1, length($3) - 1)}')"
echo "backend coverage (excluding migrations + cmd): ${total}%  (gate: ${min}%)"

awk -v t="$total" -v min="$min" 'BEGIN {
	if (t + 0 < min + 0) {
		printf "::error::backend coverage %.1f%% is below the %s%% gate\n", t, min
		exit 1
	}
}'
