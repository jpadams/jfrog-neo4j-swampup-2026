#!/usr/bin/env bash
# Shared connection resolution for the graph scripts. Source, do not execute.
#
# Precedence: real environment > .env at the repo root > local container default.
# That way the same scripts work against the local Docker instance and against
# Aura, with no flags and no duplicated credentials.
#
# Sets: NEO4J_URI, NEO4J_USERNAME, NEO4J_PASSWORD, NEO4J_DATABASE
# and defines run_cypher(), which reads a Cypher script on stdin.

_GRAPH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_REPO_ROOT="$(cd "$_GRAPH_DIR/.." && pwd)"

# .env must not clobber anything already exported.
if [ -f "$_REPO_ROOT/.env" ]; then
  while IFS='=' read -r key value; do
    case "$key" in
      ''|\#*) continue ;;
    esac
    if [ -z "${!key:-}" ]; then
      export "$key=$value"
    fi
  done < "$_REPO_ROOT/.env"
fi

# Announce the fallback. Silence here is how you end up querying the wrong graph:
# .env is gitignored, so a throwaway clone never has one, and sourcing this file
# from inside such a clone resolves to localhost with no indication that Aura was
# ever configured. The resulting error is a bare "connection refused" that names
# neither the missing .env nor the clone.
#
# Note that re-sourcing this file cannot undo it, by design: nothing already set
# is clobbered. Recovering needs `unset NEO4J_URI ...` first, or sourcing the
# real .env directly. Hence saying so at the point of the guess.
if [ -z "${NEO4J_URI:-}" ] && [ ! -f "$_REPO_ROOT/.env" ]; then
  echo "neo4j-env: no .env at $_REPO_ROOT and NEO4J_URI unset;" \
       "falling back to the local container." >&2
  echo "neo4j-env: if you meant Aura, source the .env from the real repo root," \
       "not a clone." >&2
fi

export NEO4J_URI="${NEO4J_URI:-neo4j://localhost:7687}"
export NEO4J_USERNAME="${NEO4J_USERNAME:-neo4j}"
export NEO4J_PASSWORD="${NEO4J_PASSWORD:-monograph2026}"
export NEO4J_DATABASE="${NEO4J_DATABASE:-neo4j}"

NEO4J_CONTAINER="${NEO4J_CONTAINER:-jfrog2026-neo4j}"
CYPHER_SHELL_IMAGE="${CYPHER_SHELL_IMAGE:-neo4j:2026.04}"

# describe_target prints a human-readable label for the current connection.
describe_target() {
  case "$NEO4J_URI" in
    *localhost*|*127.0.0.1*) echo "local container ($NEO4J_URI)" ;;
    *) echo "remote ($NEO4J_URI)" ;;
  esac
}

# run_cypher [extra cypher-shell args...] < script.cypher
#
# For a local URI it execs into the running container, which needs no image
# pull. For anything else it runs cypher-shell in a throwaway container, so no
# host install of cypher-shell is required to talk to Aura.
run_cypher() {
  case "$NEO4J_URI" in
    *localhost*|*127.0.0.1*)
      if ! docker inspect "$NEO4J_CONTAINER" >/dev/null 2>&1; then
        echo "error: container '$NEO4J_CONTAINER' not found. Run: docker compose -f $_GRAPH_DIR/docker-compose.yml up -d" >&2
        return 1
      fi
      docker exec -i "$NEO4J_CONTAINER" cypher-shell \
        -u "$NEO4J_USERNAME" -p "$NEO4J_PASSWORD" -d "$NEO4J_DATABASE" "$@"
      ;;
    *)
      docker run --rm -i "$CYPHER_SHELL_IMAGE" cypher-shell \
        -a "$NEO4J_URI" -u "$NEO4J_USERNAME" -p "$NEO4J_PASSWORD" \
        -d "$NEO4J_DATABASE" "$@"
      ;;
  esac
}
