// @monorepo/core — shared domain logic for the TypeScript apps.
//
// This is the fan-out root: libs/ui, apps/web, and apps/admin all reach it,
// so a change here must affect all of them.

import { Role, type User } from "@monorepo/proto";

export type Action = "read" | "write" | "refund";

/** Mirror of the Go libs/authz rules, for the client side. */
export function can(user: User, action: Action): boolean {
  switch (action) {
    case "read":
      return user.role !== Role.ROLE_UNSPECIFIED;
    case "write":
      return user.role === Role.ROLE_EDITOR || user.role === Role.ROLE_ADMIN;
    case "refund":
      return user.role === Role.ROLE_ADMIN;
    default:
      return false;
  }
}

/** Human-readable label for a user, used by the UI layer. */
export function displayName(user: User): string {
  return user.email.split("@")[0] ?? user.id;
}
