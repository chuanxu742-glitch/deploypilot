import type { AgentEvent } from "./agent-state";

// Metadata is printed as text, never interpreted as terminal escape sequences.
const plain = (value: string) => value.replace(/[\x00-\x08\x0b-\x1f\x7f]/g, "");
const lines = (value: string) => value.replace(/\r?\n/g, "\r\n");

export function terminalAgentOutput(event: AgentEvent): string {
  if (event.type === "output") return lines(event.text ?? "");
  if (event.type === "tool_call") {
    const location = [event.hostname, event.cwd].filter(Boolean).join("  ");
    const target = event.command ?? event.path ?? "";
    const label =
      event.tool === "run_command"
        ? "$"
        : ((
            {
              read_file: "读取",
              write_file: "写入",
              edit_file: "修改",
              append_file: "追加",
            } as Record<string, string>
          )[event.tool ?? ""] ??
          event.tool ??
          "工具");
    return `\r\n${location ? `[${plain(location)}]\r\n` : ""}${label} ${lines(plain(target))}\r\n`;
  }
  if (event.type === "tool_done") {
    const result =
      event.exitCode !== undefined
        ? `退出码 ${event.exitCode}`
        : event.success === false
          ? "执行失败"
          : "已完成";
    return `\r\n[${result}${event.durationMs !== undefined ? `  ${event.durationMs} ms` : ""}]\r\n`;
  }
  if (event.type === "error")
    return `\r\n${lines(plain(event.text ?? "执行出错"))}\r\n`;
  return "";
}
