import { memo } from "react";
import { Icon } from "./Icon";
import type { TaskSummary } from "./api";

export function shortTaskTitle(title: string): string {
  const firstLine =
    title
      .split(/\r?\n/)
      .map((line) => line.trim().replace(/\s+/g, " "))
      .find(Boolean) ?? "未命名会话";
  const characters = Array.from(firstLine);
  return characters.length > 38
    ? `${characters.slice(0, 38).join("")}…`
    : firstLine;
}

function sessionState(
  task: TaskSummary,
  globalBusy: boolean,
  globalTaskId: string | null,
  globalTaskTitle: string,
  globalRunState: "idle" | "running" | "stopping",
) {
  const isActive =
    globalBusy &&
    (globalTaskId ? task.id === globalTaskId : task.title === globalTaskTitle);
  const phase = isActive ? globalRunState : task.phase;
  if (phase === "running") return { label: "执行中", tone: "active" };
  if (phase === "stopping") return { label: "停止中", tone: "warn" };
  if (phase === "interrupted") return { label: "已中断", tone: "bad" };
  return { label: "已结束", tone: "" };
}

export const SessionSidebar = memo(function SessionSidebar({
  tasks,
  selectedId,
  busy,
  globalBusy,
  globalTaskId,
  globalTaskTitle,
  globalRunState,
  onSelect,
  onNew,
  onClose,
}: {
  tasks: TaskSummary[];
  selectedId: string | null;
  busy: boolean;
  globalBusy: boolean;
  globalTaskId: string | null;
  globalTaskTitle: string;
  globalRunState: "idle" | "running" | "stopping";
  onSelect: (id: string) => void;
  onNew: () => void;
  onClose: () => void;
}) {
  return (
    <nav className="session-nav" aria-label="会话">
      <div className="session-nav-head">
        <h2>会话</h2>
        <button
          className="icon-button session-nav-close"
          aria-label="关闭会话栏"
          title="关闭会话栏"
          onClick={onClose}
        >
          <Icon name="close" size={16} />
        </button>
      </div>
      <button className="session-new button" disabled={busy} onClick={onNew}>
        <Icon name="plus" size={16} />
        新建会话
      </button>
      <ul className="session-list">
        {tasks.length === 0 ? (
          <li className="session-empty">暂无历史会话</li>
        ) : (
          tasks.map((task) => {
            const selected = task.id === selectedId;
            const status = sessionState(
              task,
              globalBusy,
              globalTaskId,
              globalTaskTitle,
              globalRunState,
            );
            return (
              <li key={task.id}>
                <button
                  className="session-item"
                  type="button"
                  aria-current={selected ? "page" : undefined}
                  data-selected={selected}
                  data-task-id={task.id}
                  title={task.title}
                  onClick={() => onSelect(task.id)}
                >
                  <span className="session-item-title">
                    <span className="dot" data-tone={status.tone} />
                    <span>{shortTaskTitle(task.title)}</span>
                  </span>
                  <span className="session-item-meta">
                    <span>{status.label}</span>
                    <span>{new Date(task.startedAt).toLocaleDateString()}</span>
                  </span>
                </button>
              </li>
            );
          })
        )}
      </ul>
    </nav>
  );
});
