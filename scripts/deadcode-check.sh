#!/usr/bin/env bash
# Dead-code guard: fail when the deadcode analyzer reports functions that no
# binary and no test can reach. This is the class that survives golangci-lint
# (its unused check treats exported symbols as package API) and that once
# shipped — an exported signature builder nobody called — grew a stale twin
# of live code and had to be found by hand.
#
# Roots include test binaries (--test): a symbol reachable from any test is
# not dead, which keeps test-only helpers (testutils, mocks) out of the
# report. The remaining output is matched against .deadcode-allow and a
# leftover line fails the run. Allowlisted symbols that the analyzer no
# longer reports warn instead of fail: list staleness must never block.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ALLOW="$ROOT/.deadcode-allow"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$ROOT"

# The analyzer exits 0 in normal operation and nonzero on analysis failure
# (missing tool directive, compile errors). Nonzero with output is treated
# as reported findings; nonzero with empty output is the failure path.
rc=0
out="$(go tool deadcode -test ./... 2>"$TMP/err")" || rc=$?
if [ "$rc" -ne 0 ] && [ -z "$out" ]; then
	echo "deadcode: analyzer failed to run:" >&2
	sed 's/^/  /' "$TMP/err" >&2
	echo "" >&2
	echo "If the error says the tool is unknown, add it to go.mod:" >&2
	echo "  go get -tool golang.org/x/tools/cmd/deadcode" >&2
	exit 1
fi

if [ -z "$out" ]; then
	echo "deadcode: clean (nothing unreachable outside .deadcode-allow)"
	exit 0
fi

# Keep only pattern lines: comments (#) and blank lines are documentation.
if [ -f "$ALLOW" ]; then
	grep -Ev '^[[:space:]]*(#|$)' "$ALLOW" >"$TMP/patterns" 2>/dev/null || : >"$TMP/patterns"
else
	: >"$TMP/patterns"
fi

gstat=0
rest="$(printf '%s\n' "$out" | grep -Evf "$TMP/patterns")" || gstat=$?
# grep exits 2 when an allowlist pattern is an invalid regex. Falling back to
# cat there would silently disable the whole check (the reader already
# consumed the report), so anything beyond 0/1 aborts the run loudly.
if [ "$gstat" -gt 1 ]; then
	echo "deadcode: invalid regex pattern in $ALLOW (grep exit $gstat):" >&2
	cat "$TMP/patterns" | sed 's/^/  /' >&2
	exit 1
fi

# Stale allowlist entries: the analyzer stopped reporting them. Warn only —
# an emptied allowlist or a format drift must never fail the build here.
while IFS= read -r pat; do
	[ -z "$pat" ] && continue
	if ! printf '%s\n' "$out" | grep -Eq "$pat"; then
		echo "WARN: allowlist entry matches nothing anymore (remove it): $pat"
	fi
done <"$TMP/patterns"

if [ -n "$rest" ]; then
	echo "deadcode: unreachable functions found (tests count as reachable):"
	printf '%s\n' "$rest" | sed 's/^/  /'
	echo ""
	echo "Preferred fix: delete the symbol (dead code is a bug, not a todo)."
	echo "If it is alive through indirection the analyzer cannot see, copy its"
	echo "report line into .deadcode-allow as an ERE pattern with a why-comment."
	exit 1
fi

echo "deadcode: clean (nothing unreachable outside .deadcode-allow)"
