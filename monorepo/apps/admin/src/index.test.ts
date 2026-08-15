import test from "node:test";
import assert from "node:assert/strict";

import { Role, type User } from "@monorepo/proto";
import { renderConsole } from "./index.ts";

const admin: User = { id: "u-admin", email: "admin@example.com", role: Role.ROLE_ADMIN };
const viewer: User = { id: "u-viewer", email: "viewer@example.com", role: Role.ROLE_VIEWER };

test("admin console is privileged", () => {
  const view = renderConsole(admin);
  assert.equal(view.banner, "[admin] Signed in as admin");
  assert.equal(view.privileged, true);
});

test("viewer console is not privileged", () => {
  assert.equal(renderConsole(viewer).privileged, false);
});
