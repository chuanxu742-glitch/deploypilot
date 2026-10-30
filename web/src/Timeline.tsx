import { useEffect, useRef, useState } from "react";
import {
  isFailedFinal,
  type ActionItem,
  type TimelineItem,
} from "./agent-state";
import { Icon, type IconName } from "./Icon";

const toolLabels: Record<string, string> = {
  run_command: "执行命令",
  read_file: "读取文件",
  write_file: "写入文件",
  edit_file: "修改文件",
  append_file: "追加文件",
  finish: "结束任务",
};
const toolIcons: Record<string, IconName> = {
  run_command: "terminal",
  read_file: "file",
  write_file: "edit",
  edit_file: "edit",
  append_file: "edit",
};

/** An operator message: the original task or a follow-up in the same session. */
export function OperatorTurn({
  text,
  followUp = false,
}: {
  text?: string;
  followUp?: boolean;
}) {
  return (
    <li className={followUp ? "tl tl-task tl-followup" : "tl tl-task"}>
      <div className="tl-body">
        <p className="task-text">{text}</p>
      </div>
    </li>
  );
}

export function TimelineRow({
  item,
  live,
}: {
  item: TimelineItem;
  live: boolean;
}) {
  switch (item.kind) {
    case "action":
      return <ActionRow item={item} live={live} />;
    // The run status line owns the current activity; old waiting states are not log entries.
    case "activity":
      return null;
    case "note":
      if (!item.event.text) return null;
      return (
        <li className="tl tl-note">
          <div className="tl-body">{item.event.text}</div>
        </li>
      );
    case "message": {
      const text = item.event.text?.trim();
      return text ? <li className="assistant-message">{text}</li> : null;
    }
    case "user":
      return <OperatorTurn text={item.event.text} followUp />;
    case "error":
      return (
        <li className="tl tl-error">
          <div className="tl-body" role="alert">
            {item.event.text}
          </div>
        </li>
      );
    case "final": {
      const failed = isFailedFinal(item.event);
      return (
        <li className={failed ? "tl tl-final tl-error" : "tl tl-final"}>
          <div className="tl-body">
            <div className="final-card">
              <strong>{failed ? "任务未完成" : "任务结束"}</strong>
              {item.event.text && <p>{item.event.text}</p>}
            </div>
          </div>
        </li>
      );
    }
  }
  return null;
}

function actionState(item: ActionItem, live: boolean) {
  if (item.error !== undefined) return { tone: "bad", label: "出错" };
  if (item.done) {
    const code = item.done.exitCode;
    return code
      ? { tone: "bad", label: `退出码 ${code}` }
      : { tone: "ok", label: code === 0 ? "退出码 0" : "已完成" };
  }
  if (live) return { tone: "active", label: "运行中" };
  return { tone: "", label: "未确认" };
}

function ActionRow({ item, live }: { item: ActionItem; live: boolean }) {
  const [expanded, setExpanded] = useState<boolean | null>(null);
  const outputRef = useRef<HTMLPreElement>(null);
  const tool = item.call?.tool ?? item.done?.tool ?? "";
  const target = item.call?.command ?? item.call?.path ?? item.done?.path;
  const state = actionState(item, live);
  const output = item.output ?? "";
  // Live and failed actions stay open until the user chooses otherwise.
  const open = expanded ?? (live || item.error !== undefined);
  useEffect(() => {
    if (open && live && outputRef.current)
      outputRef.current.scrollTop = outputRef.current.scrollHeight;
  }, [output, open, live]);

  return (
    <li className="tl tl-action">
      <details className="action" open={open}>
        <summary
          className="action-head"
          onClick={(event) => {
            event.preventDefault();
            setExpanded(!open);
          }}
        >
          <Icon name={toolIcons[tool] ?? "terminal"} size={16} />
          <span className="action-label">
            {toolLabels[tool] ?? (tool || "命令输出")}
          </span>
          {target && !open && <code className="action-preview">{target}</code>}
          <span className="chip" data-tone={state.tone}>
            {state.label}
          </span>
          {item.done?.durationMs !== undefined && (
            <span className="action-duration">{item.done.durationMs} ms</span>
          )}
          <Icon name="chevron" size={14} />
        </summary>
        <div className="action-body">
          {(item.call?.hostname || item.call?.cwd) && (
            <div className="action-evidence">
              {item.call.hostname && <span>{item.call.hostname}</span>}
              {item.call.cwd && <span>启动目录 {item.call.cwd}</span>}
            </div>
          )}
          {target && <code className="action-target">{target}</code>}
          {output && (
            <div className="action-output">
              <pre ref={outputRef}>{output}</pre>
            </div>
          )}
          {item.error !== undefined && item.error !== "" && (
            <p className="action-error">{item.error}</p>
          )}
        </div>
      </details>
    </li>
  );
}
