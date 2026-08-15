#!/bin/sh
# Minimal, real validation for the infra target: every declared service must
# have a replica count, and the environment must be one we recognise.
# Deliberately dependency-free so it runs in a bare alpine container.
set -eu

HERE="$(dirname "$0")"
TF="$HERE/main.tf"

[ -f "$TF" ] || { echo "infra: main.tf missing"; exit 1; }

services=$(grep -c '^resource "null_service"' "$TF" || true)
replicas=$(grep -c '^  replicas' "$TF" || true)

if [ "$services" -ne "$replicas" ]; then
  echo "infra: $services service(s) but $replicas replica declaration(s)"
  exit 1
fi

grep -q 'default = "staging"' "$TF" || { echo "infra: unexpected default environment"; exit 1; }

echo "infra: ok ($services services validated)"
