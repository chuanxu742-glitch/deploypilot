import { test } from "node:test";
import assert from "node:assert/strict";
import { terminalAgentOutput } from "../src/terminal-agent.ts";

test("terminal shows actual commands, streamed output and zero exit code", () => {
  assert.equal(terminalAgentOutput({ type: "tool_call", tool: "run_command", command: "pwd", hostname: "node-a", cwd: "/srv/app" }), "\r\n[node-a  /srv/app]\r\n$ pwd\r\n");
  assert.equal(terminalAgentOutput({ type: "output", text: "hello\nworld\r\n" }), "hello\r\nworld\r\n");
  assert.equal(terminalAgentOutput({ type: "tool_done", tool: "run_command", exitCode: 0, durationMs: 42 }), "\r\n[退出码 0  42 ms]\r\n");
  assert.equal(terminalAgentOutput({ type: "thinking", text: "public assistant text" }), "");
  assert.equal(terminalAgentOutput({ type: "activity", state: "generating" }), "");
});

test("terminal displays file operations and errors without executing metadata control codes", () => {
  assert.equal(terminalAgentOutput({ type: "tool_call", tool: "write_file", path: "/srv/test" }), "\r\n写入 /srv/test\r\n");
  assert.doesNotMatch(terminalAgentOutput({ type: "tool_call", command: "\x1b[2J", hostname: "bad\x1b" }), /\x1b/);
  assert.match(terminalAgentOutput({ type: "tool_done", success: false }), /执行失败/);
  assert.match(terminalAgentOutput({ type: "error", text: "failed" }), /failed/);
});
