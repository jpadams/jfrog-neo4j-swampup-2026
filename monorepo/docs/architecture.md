# Architecture notes

## The user contract

`proto/user.proto` is the single source of truth for the `User` shape and its
`Role` enum. Both language stacks consume code generated from it:

- Go, via `proto/gen/go/userpb`
- TypeScript, via the `@monorepo/proto` workspace package at `proto/gen/ts`

Because both stacks import the proto target through ordinary imports, a change
to `user.proto` fans out across the language boundary. That is the third golden
test.

## Authorization

The rules are deliberately duplicated: `libs/authz` (Go, authoritative, server
side) and `libs/core` (TypeScript, advisory, used to hide UI affordances the
user cannot use). A real system would generate one from the other; here the
duplication gives the graph two independent fan-out roots, one per language.

## Services

`services/api` handles user-facing requests. `services/billing` issues refunds
and is the only target nothing else depends on, which makes it the leaf used to
check for false positives.
