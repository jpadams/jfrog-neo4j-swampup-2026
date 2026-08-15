import test from "node:test";
import assert from "node:assert/strict";

import { Role, type User } from "@monorepo/proto";
import { header, visibleActions } from "./index.ts";

const admin: User = { id: "u-admin", email: "admin@example.com", role: Role.ROLE_ADMIN };
const viewer: User = { id: "u-viewer", email: "viewer@example.com", role: Role.ROLE_VIEWER };

test("header greets by display name", () => {
  assert.equal(header(viewer), "Signed in as viewer");
});

test("admin sees every action", () => {
  assert.deepEqual(visibleActions(admin), ["View", "Edit", "Refund"]);
});

test("viewer sees only View", () => {
  assert.deepEqual(visibleActions(viewer), ["View"]);
});
