import { test } from "node:test";
import assert from "node:assert/strict";
import {
  applyAgentEvent,
  buildTimeline,
  mergeEvent,
  MAX_EVENTS,
  MAX_EVENT_TEXT,
  TRUNCATED,
} from "../src/agent-state.ts";
import { clampSplitRatio, resolveTheme } from "../src/ui-preferences.ts";

test("theme defaults to night and preserves saved day/night/comfort", () => {
  assert.equal(resolveTheme(null), "night");
  assert.equal(resolveTheme("unknown"), "night");
  for (const theme of ["day", "night", "comfort"] as const)
    assert.equal(resolveTheme(theme), theme);
});

test("desktop split ratio stays inside usable task and terminal widths", () => {
  assert.equal(clampSplitRatio(0.6), 0.6);
  assert.equal(clampSplitRatio(0.1), 0.4);
  assert.equal(clampSplitRatio(0.99), 0.75);
  assert.equal(clampSplitRatio(0.75, 900), 0.6);
});

test("a recoverable tool error does not finish a running task", () => {
  assert.equal(
    applyAgentEvent("running", { type: "error", text: "命令被拦截" }),
    "running",
  );
});
test("a final summary does not release the task before cleanup acknowledgement", () => {
  assert.equal(applyAgentEvent("stopping", { type: "final" }), "stopping");
  assert.equal(
    applyAgentEvent("stopping", { type: "run_state", state: "idle" }),
    "finished",
  );
});
test("server acknowledgements update running and stopping states", () => {
  assert.equal(
    applyAgentEvent("connecting", { type: "run_state", state: "running" }),
    "running",
  );
  assert.equal(
    applyAgentEvent("running", { type: "run_state", state: "stopping" }),
    "stopping",
  );
});
test("streamed output retains its recent tail with an explicit truncation marker", () => {
  const result = mergeEvent(
    [{ type: "output", text: "x".repeat(MAX_EVENT_TEXT) }],
    { type: "output", text: "尾部" },
  );
  assert.equal(result.length, 1);
  assert.ok(result[0].text!.length <= MAX_EVENT_TEXT);
  assert.ok(result[0].text!.startsWith(TRUNCATED));
  assert.ok(result[0].text!.endsWith("尾部"));
});
test("event count and a single oversized message are bounded", () => {
  let events = [] as ReturnType<typeof mergeEvent>;
  for (let i = 0; i < MAX_EVENTS + 10; i++)
    events = mergeEvent(events, { type: "status", step: i });
  assert.equal(events.length, MAX_EVENTS);
  assert.equal(events[0].step, 10);
  assert.ok(
    mergeEvent([], {
      type: "thinking",
      text: "x".repeat(MAX_EVENT_TEXT + 1),
    })[0].text!.length <= MAX_EVENT_TEXT,
  );
});
test("merging output preserves boundaries around tool events", () => {
  const events = mergeEvent([{ type: "tool_call", tool: "run_command" }], {
    type: "output",
    text: "ok",
  });
  assert.equal(events.length, 2);
  assert.equal(events[0].tool, "run_command");
});

test("real activity and execution evidence remain distinct and grouped by call id", () => {
  const events = [
    { type: "activity", state: "generating", text: "等待模型生成", timestamp: 100 },
    { type: "tool_call", tool: "run_command", callId: "c1", hostname: "node-a", cwd: "/srv/app", timestamp: 200 },
    { type: "output", callId: "c1", text: "ready", timestamp: 250 },
    { type: "tool_done", callId: "c1", durationMs: 50, exitCode: 0, timestamp: 300 },
  ];
  const timeline = buildTimeline(events);
  assert.equal(timeline[0].kind, "activity");
  assert.equal(timeline[0].event.state, "generating");
  assert.equal(timeline[1].kind, "action");
  if (timeline[1].kind !== "action") throw new Error("expected action row");
  assert.equal(timeline[1].call?.callId, "c1");
  assert.equal(timeline[1].call?.hostname, "node-a");
  assert.equal(timeline[1].call?.cwd, "/srv/app");
  assert.equal(timeline[1].output, "ready");
  assert.equal(timeline[1].done?.durationMs, 50);
});

test("public assistant content remains a short progress message", () => {
  const timeline = buildTimeline([{ type: "thinking", text: "我会先检查监听端口" }]);
  assert.equal(timeline[0].kind, "message");
  assert.equal(timeline[0].event.text, "我会先检查监听端口");
});

test("tool error after completion remains attached to its call and overrides success", () => {
  const timeline = buildTimeline([
    { type: "tool_call", tool: "run_command", callId: "c2" },
    { type: "activity", state: "executing", text: "执行工具" },
    { type: "output", callId: "c2", text: "partial" },
    { type: "tool_done", callId: "c2", durationMs: 0, exitCode: 0 },
    { type: "error", callId: "c2", text: "清理失败" },
  ]);
  assert.equal(timeline.filter((item) => item.kind === "action").length, 1);
  const action = timeline.find((item) => item.kind === "action");
  assert.equal(action?.kind, "action");
  if (action?.kind === "action") assert.equal(action.error, "清理失败");
});
