import assert from "node:assert/strict";
import test from "node:test";

import { isConnected } from "./connection.js";

test("only exposes the consensus console after a successful connection", () => {
  assert.equal(isConnected(), false);
  assert.equal(isConnected({ phase: "checking" }), false);
  assert.equal(isConnected({ phase: "authentication_required" }), false);
  assert.equal(isConnected({ phase: "unavailable" }), false);
  assert.equal(isConnected({ phase: "connected" }), true);
});
