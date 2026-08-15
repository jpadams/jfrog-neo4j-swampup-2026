import test from "node:test";
import assert from "node:assert/strict";

import { Role, type User } from "@monorepo/proto";
import { renderDashboard } from "./index.ts";

const editor: User = { id: "u-editor", email: "editor@example.com", role: Role.ROLE_EDITOR };

test("editor dashboard shows edit affordances", () => {
  const page = renderDashboard(editor);
  assert.equal(page.title, "Signed in as editor");
  assert.deepEqual(page.actions, ["View", "Edit"]);
  assert.equal(page.canEdit, true);
});
