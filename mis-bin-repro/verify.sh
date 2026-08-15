#!/usr/bin/env bash
# Show that `neo4jUri` is unreachable, and that its advertised flag silently
# fills `neo4juri` when both arguments exist.
#
#   ./verify.sh                        # dagger on PATH
#   DAGGER=/path/to/dagger ./verify.sh
#
# Exit 0 = bug reproduced. Exit 1 = not reproduced (possibly fixed).
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DAGGER="${DAGGER:-dagger}"
MOD="$HERE/mangle"

command -v "$DAGGER" >/dev/null || { echo "error: dagger not found; set DAGGER=/path/to/dagger" >&2; exit 1; }

HARD_ERRORS=0
MISBINDINGS=0

rule() { printf '%s\n' "------------------------------------------------------------"; }

# run <function> <flag> <value>
# Prints the call and its outcome; sets RESULT and RC for the caller.
run() {
  local fn="$1" flag="$2" value="$3" out
  out="$("$DAGGER" -m "$MOD" call "$fn" "$flag=$value" 2>&1)"; RC=$?
  if [ $RC -ne 0 ]; then
    RESULT="$(printf '%s' "$out" | grep -oE 'find arg "[^"]*"' | head -1)"
    RESULT="ERROR: ${RESULT:-unknown failure}"
  else
    RESULT="$(printf '%s' "$out" | grep -E '="' | tail -1)"
  fi
  echo "        \$ dagger call $fn $flag=$value"
  echo "          $RESULT"
}

echo
echo "dagger version: $("$DAGGER" version 2>/dev/null | head -1 | awk '{print $2}')"
echo "module:         $MOD/main.dang"
rule

echo "STEP 1  Two argument names differing only by one letter's case, used across"
echo "        three functions:"
echo
echo "          neo4juri   all lowercase"
echo "          neo4jUri   capital U"
echo
"$DAGGER" functions -m "$MOD" 2>/dev/null | tail -3 | sed 's/^/          /'
rule

echo "STEP 2  dagger's own view of the names. Source -> GraphQL schema -> flag."
echo
API_ARGS="$(printf '%s' '{ __type(name: "Mangle") { fields { name args { name } } } }' \
  | "$DAGGER" -m "$MOD" query 2>/dev/null \
  | tr -d ' \n' | grep -oE '"neo4J[A-Za-z]*"' | tr -d '"' | sort -u | paste -sd, -)"
echo "          GraphQL schema names (via introspection): ${API_ARGS:-<unavailable>}"
echo
echo "          source      schema      advertised flag   should set"
echo "          ----------  ----------  ----------------  ----------"
echo "          neo4juri    neo4Juri    --neo-4-juri      neo4juri"
echo "          neo4jUri    neo4JUri    --neo-4-j-uri     neo4jUri"
rule

echo "STEP 3  CONTROL. A function declaring ONLY the lowercase argument."
echo "        Its flag should work:"
echo
run probe-only-lowercase --neo-4-juri ONLY_LOWERCASE
if [ "$RC" -eq 0 ]; then echo "          => correct"; else echo "          => UNEXPECTED failure"; fi
rule

echo "STEP 4  A function declaring ONLY the capital-U argument, called with the"
echo "        flag dagger advertises for it. Nothing else can absorb the value:"
echo
run probe-only-capital --neo-4-j-uri ONLY_CAPITAL
if [ "$RC" -ne 0 ]; then
  HARD_ERRORS=$((HARD_ERRORS + 1))
  echo "          => UNREACHABLE: the argument's own advertised flag fails"
else
  echo "          => unexpectedly succeeded; this version may be fixed"
fi
rule

echo "STEP 5  A function declaring BOTH. Sentinel values name their intended"
echo "        target, so the outcome needs no interpretation:"
echo
run probe --neo-4-juri MEANT_FOR_neo4juri
echo
run probe --neo-4-j-uri MEANT_FOR_neo4jUri
case "$RESULT" in
  *'neo4juri="MEANT_FOR_neo4jUri"'*)
    MISBINDINGS=$((MISBINDINGS + 1))
    echo "          => MIS-BOUND: the value meant for neo4jUri landed in neo4juri,"
    echo "             neo4jUri stayed empty, and the command exited 0"
    ;;
  *) echo "          => not mis-bound; this version may be fixed" ;;
esac
rule

echo "RESULT"
echo
if [ "$HARD_ERRORS" -ge 1 ] && [ "$MISBINDINGS" -ge 1 ]; then
  cat <<'TXT'
        BUG REPRODUCED, in two forms.

        1. Alone, neo4jUri is unreachable -- the flag dagger advertises for it
           fails with: find arg "neo4JUri".
        2. Beside neo4juri, that same flag silently fills neo4juri instead and
           exits 0.

        Form 2 is the dangerous one: an argument can be silently unreachable
        while a different argument absorbs its input. Were the intended argument
        a Secret, a credential would be routed to the wrong parameter and the
        build would still be green.
TXT
  echo
  echo "        exit 0 (bug present)"
  exit 0
fi

echo "        NOT fully reproduced (hard errors: $HARD_ERRORS, mis-bindings: $MISBINDINGS)."
echo "        This dagger version may contain a fix."
echo
echo "        exit 1"
exit 1
