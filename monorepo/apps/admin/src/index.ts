// @monorepo/admin — the internal console. Depends on ui only, so it reaches
// core transitively. That asymmetry with apps/web is deliberate: it proves the
// extractor walks transitive edges rather than only direct ones.

import { header, visibleActions } from "@monorepo/ui";
import type { User } from "@monorepo/proto";

export interface AdminView {
  banner: string;
  controls: string[];
  privileged: boolean;
}

export function renderConsole(user: User): AdminView {
  const controls = visibleActions(user);
  return {
    banner: `[admin] ${header(user)}`,
    controls,
    privileged: controls.includes("Refund"),
  };
}
