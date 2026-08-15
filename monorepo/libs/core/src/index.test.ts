import test from "node:test";
import assert from "node:assert/strict";

import { Role, type User } from "@monorepo/proto";
import { can, displayName } from "./index.ts";

const admin: User = { id: "u-admin", email: "admin@example.com", role: Role.ROLE_ADMIN };
const viewer: User = { id: "u-viewer", email: "viewer@example.com", role: Role.ROLE_VIEWER };

test("viewer can read but not write", () => {
  assert.equal(can(viewer, "read"), true);
  assert.equal(can(viewer, "write"), false);
});

test("only admin can refund", () => {
  assert.equal(can(admin, "refund"), true);
  assert.equal(can(viewer, "refund"), false);
});

test("displayName uses the email local part", () => {
  assert.equal(displayName(admin), "admin");
});
