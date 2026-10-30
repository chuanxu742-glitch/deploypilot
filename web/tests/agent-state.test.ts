import { test } from "node:test";
import assert from "node:assert/strict";
import {
  applyAgentEvent,
  buildTimeline,
  mergeEvent,
  normalizeEvents,
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
    {
      type: "activity",
      state: "generating",
      text: "等待模型生成",
      timestamp: 100,
    },
    {
      type: "tool_call",
      tool: "run_command",
      callId: "c1",
      hostname: "node-a",
      cwd: "/srv/app",
      timestamp: 200,
    },
    { type: "output", callId: "c1", text: "ready", timestamp: 250 },
    {
      type: "tool_done",
      callId: "c1",
      durationMs: 50,
      exitCode: 0,
      timestamp: 300,
    },
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
  const timeline = buildTimeline([
    { type: "thinking", text: "我会先检查监听端口" },
  ]);
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

test("command timeout and cancellation metadata stay on their action rows", () => {
  const callId = "command-state";
  const timeline = buildTimeline([
    {
      type: "tool_call",
      tool: "run_command",
      callId,
      timestamp: 100,
      timeoutSeconds: 600,
    },
    {
      type: "tool_done",
      callId,
      exitCode: -1,
      state: "cancelled",
      durationMs: 374000,
      logPath: "C:/private/command.log",
      text: "命令已取消",
    },
  ]);
  const action = timeline.find((item) => item.kind === "action");
  assert.equal(action?.kind, "action");
  if (action?.kind === "action") {
    assert.equal(action.call?.timeoutSeconds, 600);
    assert.equal(action.call?.timestamp, 100);
    assert.equal(action.done?.state, "cancelled");
    assert.equal(action.done?.logPath, "C:/private/command.log");
    assert.equal(action.done?.durationMs, 374000);
  }

  const timedOut = buildTimeline([
    { type: "tool_call", tool: "run_command", callId },
    {
      type: "error",
      callId,
      state: "timeout",
      text: "命令已超过 600 秒时限",
      logPath: "C:/private/timeout.log",
    },
  ]).find((item) => item.kind === "action");
  assert.equal(timedOut?.kind, "action");
  if (timedOut?.kind === "action") {
    assert.equal(timedOut.errorState, "timeout");
    assert.equal(timedOut.error, "命令已超过 600 秒时限");
    assert.equal(timedOut.logPath, "C:/private/timeout.log");
  }
});

test("wait_for progress replaces prior activity and stays linked to its action", () => {
  const taskId = "task-wait";
  const callId = "call-wait";
  const events = normalizeEvents([
    { id: 1, taskId, type: "tool_call", tool: "wait_for", callId, step: 1 },
    {
      id: 2,
      taskId,
      type: "activity",
      state: "waiting",
      tool: "wait_for",
      callId,
      text: "等待 HTTP 服务，已等待 5 秒",
    },
    {
      id: 3,
      taskId,
      type: "activity",
      state: "waiting",
      tool: "wait_for",
      callId,
      text: "等待 HTTP 服务，已等待 10 秒；最近观察：连接被拒绝",
    },
    { id: 4, taskId, type: "output", callId, text: "HTTP 200，条件已满足" },
    { id: 5, taskId, type: "tool_done", callId, exitCode: 0 },
  ]);
  const waiting = events.filter(
    (event) => event.type === "activity" && event.state === "waiting",
  );
  assert.equal(waiting.length, 1);
  assert.equal(
    waiting[0].text,
    "等待 HTTP 服务，已等待 10 秒；最近观察：连接被拒绝",
  );

  const timeline = buildTimeline(events);
  const activity = timeline.find((item) => item.kind === "activity");
  assert.equal(activity?.kind, "activity");
  if (activity?.kind === "activity")
    assert.equal(activity.event.text, waiting[0].text);
  const action = timeline.find((item) => item.kind === "action");
  assert.equal(action?.kind, "action");
  if (action?.kind === "action") {
    assert.equal(action.call?.callId, callId);
    assert.equal(action.progress, waiting[0].text);
    assert.equal(action.output, "HTTP 200，条件已满足");
    assert.equal(action.done?.exitCode, 0);
  }

  const anotherTask = normalizeEvents([
    { ...waiting[0], taskId: "other-task" },
    { ...waiting[0], id: 6, taskId: "task-wait" },
  ]);
  assert.equal(anotherTask.length, 2);
});
