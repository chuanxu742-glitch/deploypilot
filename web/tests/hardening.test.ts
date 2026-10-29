import { test } from "node:test";
import assert from "node:assert/strict";
import { isFailedFinal, type AgentEvent } from "../src/agent-state.ts";
import {
  onSessionExpired,
  notifySessionExpired,
  SESSION_EXPIRED_CODE,
} from "../src/api.ts";

test("isFailedFinal flags only an explicit failed final event", () => {
  assert.equal(isFailedFinal({ type: "final", success: false } as AgentEvent), true);
  assert.equal(isFailedFinal({ type: "final", success: true } as AgentEvent), false);
  assert.equal(isFailedFinal({ type: "final" } as AgentEvent), false);
  assert.equal(isFailedFinal({ type: "tool_done", success: false } as AgentEvent), false);
});

test("session-expired code matches the server's application close code", () => {
  assert.equal(SESSION_EXPIRED_CODE, 4401);
});

test("session-expired handler fires until cleared", () => {
  let count = 0;
  onSessionExpired(() => {
    count += 1;
  });
  notifySessionExpired();
  notifySessionExpired();
  assert.equal(count, 2);
  onSessionExpired(null);
  notifySessionExpired();
  assert.equal(count, 2);
});
