# Infrastructure for the demo monorepo.
#
# Nothing in the code graph depends on infra, and infra depends on nothing —
# it is a disconnected component, which is exactly what makes it useful for
# proving the extractor does not invent edges.

variable "environment" {
  type    = string
  default = "staging"
}

resource "null_service" "api" {
  name     = "api"
  replicas = 2
}

resource "null_service" "billing" {
  name     = "billing"
  replicas = 1
}
