// @monorepo/web — the customer-facing app. Depends on ui and core.

import { header, visibleActions } from "@monorepo/ui";
import { can } from "@monorepo/core";
import type { User } from "@monorepo/proto";

export interface Page {
  title: string;
  actions: string[];
  canEdit: boolean;
}

export function renderDashboard(user: User): Page {
  return {
    title: header(user),
    actions: visibleActions(user),
    canEdit: can(user, "write"),
  };
}
