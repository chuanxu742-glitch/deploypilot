import { memo, useEffect, useRef, useState } from "react";
import {
  isFailedFinal,
  type ActionItem,
  type TimelineItem,
} from "./agent-state";
import { Icon, type IconName } from "./Icon";
import { Markdown } from "./Markdown";

const toolLabels: Record<string, string> = {
  run_command: "执行命令",
  read_file: "读取文件",
  write_file: "写入文件",
  edit_file: "修改文件",
  append_file: "追加文件",
  wait_for: "等待条件",
  finish: "结束任务",
};
const toolIcons: Record<string, IconName> = {
  run_command: "terminal",
  read_file: "file",
  write_file: "edit",
  edit_file: "edit",
  append_file: "edit",
  wait_for: "activity",
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
      const text = item.event.text;
      return text?.trim() ? (
        <li className="assistant-message">
          <Markdown>{text}</Markdown>
        </li>
      ) : null;
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
      if (item.event.state === "reply") return null;
      const needsInput = item.event.state === "needs_input";
      const failed = isFailedFinal(item.event);
      return (
        <li className={failed ? "tl tl-final tl-error" : "tl tl-final"}>
          <div className="tl-body">
            <div className="final-card">
              <strong>
                {needsInput
                  ? "等待补充信息"
                  : failed
                    ? "任务未完成"
                    : "任务结束"}
              </strong>
              {item.event.text && <Markdown>{item.event.text}</Markdown>}
            </div>
          </div>
        </li>
      );
    }
  }
  return null;
}

// Draft changes must not reconcile the entire history on each keystroke.
export const TimelineList = memo(function TimelineList({
  items,
  submittedTask,
  truncated,
  busy,
  liveActionKey,
}: {
  items: TimelineItem[];
  submittedTask?: string;
  truncated: boolean;
  busy: boolean;
  liveActionKey?: string | number;
}) {
  return (
    <ol className="timeline">
      <OperatorTurn text={submittedTask} />
      {truncated && (
        <li className="tl tl-note">
          <div className="tl-body">较早记录已截断。</div>
        </li>
      )}
      {items.map((item) => (
        <TimelineRow
          key={item.key}
          item={item}
          live={busy && item.kind === "action" && item.key === liveActionKey}
        />
      ))}
    </ol>
  );
});

function actionState(item: ActionItem, live: boolean) {
  if (isCancelledAction(item)) return { tone: "", label: "已取消" };
  if (item.errorState === "timeout" || item.done?.state === "timeout")
    return { tone: "bad", label: "超时" };
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

function isCancelledAction(item: ActionItem): boolean {
  if (item.errorState === "cancelled" || item.done?.state === "cancelled")
    return true;
  if (item.errorState || item.done?.state) return false;
  // Older saved command errors lack a state; keep the compatibility match precise.
  return isLegacyCancelledDiagnostic(item.error ?? item.done?.text ?? "");
}

function isLegacyCancelledDiagnostic(text: string): boolean {
  return /^exit=-1\s+error=command cancelled:\s*context canceled(?:\s+output:|\r?\n|$)/i.test(
    text,
  );
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.floor(ms))}毫秒`;
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours) return `${hours}小时${minutes}分${seconds}秒`;
  if (minutes) return `${minutes}分${seconds}秒`;
  return `${seconds}秒`;
}

function ActionRow({ item, live }: { item: ActionItem; live: boolean }) {
  const [expanded, setExpanded] = useState<boolean | null>(null);
  const [now, setNow] = useState(Date.now);
  const outputRef = useRef<HTMLPreElement>(null);
  const outputFollowRef = useRef(true);
  const [outputFollowing, setOutputFollowing] = useState(true);
  const startedAtRef = useRef(item.call?.timestamp ?? Date.now());
  const tool = item.call?.tool ?? item.done?.tool ?? "";
  const target = item.call?.command ?? item.call?.path ?? item.done?.path;
  const state = actionState(item, live);
  const output = item.output ?? "";
  const running = live && !item.done && item.error === undefined;
  const cancelled = isCancelledAction(item);
  const legacyCancellation =
    cancelled &&
    !item.errorState &&
    !item.done?.state &&
    isLegacyCancelledDiagnostic(item.error ?? item.done?.text ?? "");
  const timedOut =
    item.errorState === "timeout" || item.done?.state === "timeout";
  const legacyLogPath = legacyCancellation
    ? (item.error ?? item.done?.text ?? "").match(
        /\[full output:\s*([^\]]+)\]/i,
      )?.[1]
    : undefined;
  const logPath = item.logPath ?? item.done?.logPath ?? legacyLogPath;
  const resultText = cancelled
    ? legacyCancellation
      ? "命令已取消"
      : (item.done?.text ?? item.error ?? "命令已取消")
    : timedOut
      ? (item.done?.text ?? item.error ?? "命令执行超时")
      : undefined;
  // Live and failed actions stay open until the user chooses otherwise.
  const open = expanded ?? (live || cancelled || item.error !== undefined);
  useEffect(() => {
    if (open && live && outputFollowRef.current && outputRef.current)
      outputRef.current.scrollTop = outputRef.current.scrollHeight;
  }, [output, open, live]);
  useEffect(() => {
    if (!running) return;
    startedAtRef.current = item.call?.timestamp ?? Date.now();
    const tick = () => setNow(Date.now());
    tick();
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, [running, item.call?.timestamp]);

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
            <span className="action-duration">
              {formatDuration(item.done.durationMs)}
            </span>
          )}
          <Icon name="chevron" size={14} />
        </summary>
        <div className="action-body">
          {running && tool === "run_command" && (
            <div className="action-evidence action-running-meta" role="status">
              <span>已运行 {formatDuration(now - startedAtRef.current)}</span>
              {item.call?.timeoutSeconds !== undefined && (
                <span>
                  限时 {formatDuration(item.call.timeoutSeconds * 1000)}
                </span>
              )}
            </div>
          )}
          {(item.call?.hostname || item.call?.cwd) && (
            <div className="action-evidence">
              {item.call.hostname && <span>{item.call.hostname}</span>}
              {item.call.cwd && <span>启动目录 {item.call.cwd}</span>}
            </div>
          )}
          {target && <code className="action-target">{target}</code>}
          {live && !item.done && item.error === undefined && item.progress && (
            <div className="action-evidence action-progress" role="status">
              {item.progress}
            </div>
          )}
          {output && (
            <div className="action-output">
              <pre
                ref={outputRef}
                onScroll={(event) => {
                  const element = event.currentTarget;
                  const following =
                    element.scrollHeight -
                      element.scrollTop -
                      element.clientHeight <
                    24;
                  outputFollowRef.current = following;
                  setOutputFollowing(following);
                }}
              >
                {output}
              </pre>
              {live && !outputFollowing && (
                <button
                  className="text-button"
                  onClick={() => {
                    outputFollowRef.current = true;
                    setOutputFollowing(true);
                    if (outputRef.current)
                      outputRef.current.scrollTop =
                        outputRef.current.scrollHeight;
                  }}
                >
                  返回最新输出
                </button>
              )}
            </div>
          )}
          {item.error !== undefined &&
            item.error !== "" &&
            !cancelled &&
            !timedOut && <p className="action-error">{item.error}</p>}
          {resultText && (
            <p className={cancelled ? "action-result-note" : "action-error"}>
              {resultText}
            </p>
          )}
          {logPath && (
            <div className="action-evidence action-log-path">
              <span>完整日志</span>
              <code>{logPath}</code>
            </div>
          )}
        </div>
      </details>
    </li>
  );
}
