// @monorepo/ui — presentation helpers shared by both apps.

import { can, displayName } from "@monorepo/core";
import type { User } from "@monorepo/proto";

/** Render the greeting shown in a page header. */
export function header(user: User): string {
  return `Signed in as ${displayName(user)}`;
}

/** Which buttons the UI should show for this user. */
export function visibleActions(user: User): string[] {
  const actions: string[] = [];
  if (can(user, "read")) actions.push("View");
  if (can(user, "write")) actions.push("Edit");
  if (can(user, "refund")) actions.push("Refund");
  return actions;
}
