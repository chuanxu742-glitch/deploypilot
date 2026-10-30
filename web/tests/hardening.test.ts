import { test } from "node:test";
import assert from "node:assert/strict";
import {
  buildTimeline,
  isFailedFinal,
  normalizeEvents,
  type AgentEvent,
} from "../src/agent-state.ts";
import {
  onSessionExpired,
  notifySessionExpired,
  SESSION_EXPIRED_CODE,
} from "../src/api.ts";

test("isFailedFinal flags only an explicit failed final event", () => {
  assert.equal(
    isFailedFinal({ type: "final", success: false } as AgentEvent),
    true,
  );
  assert.equal(
    isFailedFinal({ type: "final", success: true } as AgentEvent),
    false,
  );
  assert.equal(isFailedFinal({ type: "final" } as AgentEvent), false);
  assert.equal(
    isFailedFinal({ type: "final", state: "reply", success: false } as AgentEvent),
    false,
  );
  assert.equal(
    isFailedFinal({ type: "final", state: "needs_input", success: false } as AgentEvent),
    false,
  );
  assert.equal(
    isFailedFinal({ type: "tool_done", success: false } as AgentEvent),
    false,
  );
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

test("a follow-up in the same session renders as its own operator row", () => {
  const timeline = buildTimeline([
    { type: "thinking", text: "第一次回复" },
    { type: "final", success: true, text: "完成" },
    { type: "user", text: "当前电脑有python环境吗？" },
    { type: "thinking", text: "有的" },
    { type: "thinking", text: "，Python 3.11.9。" },
  ]);
  assert.deepEqual(
    timeline.map((i) => i.kind),
    ["message", "final", "user", "message"],
  );
  const user = timeline[2];
  assert.equal(
    user.kind === "user" && user.event.text,
    "当前电脑有python环境吗？",
  );
  const reply = timeline[3];
  assert.equal(
    reply.kind === "message" && reply.event.text,
    "有的，Python 3.11.9。",
  );
});

test("stored streaming fragments render as one reply, not one line each", () => {
  const stored: AgentEvent[] = [
    { id: 1, type: "status", text: "在上一个任务的上下文上继续执行。" },
    { id: 2, type: "thinking", text: "有的" },
    { id: 3, type: "thinking", text: "，当前" },
    { id: 4, type: "thinking", text: "电脑已安装 Pyth" },
    { id: 5, type: "thinking", text: "on 3.11.9。" },
  ];
  for (const events of [stored, normalizeEvents(stored)]) {
    const messages = buildTimeline(events).filter(
      (item) => item.kind === "message",
    );
    assert.equal(messages.length, 1);
    assert.equal(
      messages[0].kind === "message" && messages[0].event.text,
      "有的，当前电脑已安装 Python 3.11.9。",
    );
  }
  assert.equal(normalizeEvents(stored).length, 2);
});

test("replies separated by a tool call stay separate", () => {
  const timeline = buildTimeline([
    { type: "thinking", text: "先检查" },
    { type: "tool_call", tool: "run_command", callId: "c1" },
    { type: "tool_done", tool: "run_command", callId: "c1", exitCode: 0 },
    { type: "thinking", text: "检查完成" },
  ]);
  assert.deepEqual(
    timeline
      .filter((i) => i.kind === "message")
      .map((i) => i.kind === "message" && i.event.text),
    ["先检查", "检查完成"],
  );
});
