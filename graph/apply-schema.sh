#!/usr/bin/env bash
# Apply graph/schema.cypher to the configured Neo4j (local container or Aura).
# Idempotent — every statement uses IF NOT EXISTS.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=graph/neo4j-env.sh
. "$HERE/neo4j-env.sh"

echo "applying schema to $(describe_target)"
# cypher-shell handles the multi-statement file directly; neo4j-cli takes one
# statement at a time and is used for ad-hoc queries instead.
run_cypher --format plain < "$HERE/schema.cypher"
echo "schema applied"
