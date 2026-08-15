#!/usr/bin/env bash
# Run a named query from graph/queries/ against the configured Neo4j.
#
#   ./graph/query.sh              list available queries
#   ./graph/query.sh slowest      run graph/queries/slowest.cypher
#
# These are plain .cypher files on purpose: the point of putting CI history in a
# graph is that a new question is a query someone can read and edit, not a code
# change. Run them in Neo4j Browser just as happily.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ $# -eq 0 ]; then
  echo "available queries:"
  for f in "$HERE"/queries/*.cypher; do
    name="$(basename "$f" .cypher)"
    desc="$(head -1 "$f" | sed 's|^// *||')"
    printf '  %-12s %s\n' "$name" "$desc"
  done
  exit 0
fi

QUERY="$HERE/queries/$1.cypher"
if [ ! -f "$QUERY" ]; then
  echo "error: no such query '$1'. Run with no arguments to list them." >&2
  exit 1
fi
shift

# shellcheck source=graph/neo4j-env.sh
. "$HERE/neo4j-env.sh"
run_cypher --format plain "$@" < "$QUERY"
