import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { getTask, listTasks, llmStatus, notifySessionExpired, wsURL, SESSION_EXPIRED_CODE, type AgentSnapshot, type LLMStatus, type TaskRecord, type TaskSummary } from "./api";
import {
  applyAgentEvent,
  buildTimeline,
  defaultContinue,
  isFailedFinal,
  mergeEvent,
  normalizeEvents,
  MAX_EVENTS,
  TRUNCATED,
  type ActionItem,
  type AgentEvent,
  type TaskPhase,
  type TimelineItem,
} from "./agent-state";
import { Icon, type IconName } from "./Icon";
import { Settings } from "./Settings";

const phaseLabels: Record<TaskPhase, string> = {
  idle: "待执行",
  connecting: "连接中",
  running: "执行中",
  stopping: "停止中",
  finished: "已结束",
  interrupted: "进程重启后中断",
  disconnected: "连接断开",
};
export const phaseTone: Record<TaskPhase, string> = {
  idle: "",
  connecting: "active",
  running: "active",
  stopping: "warn",
  finished: "",
  interrupted: "bad",
  disconnected: "bad",
};
const examples: { label: string; task: string; icon: IconName }[] = [
  { label: "环境", task: "检查目标机器的系统版本、磁盘、内存和 GPU 环境", icon: "system" },
  { label: "部署项目", task: "帮我检查当前项目并完成部署", icon: "box" },
  { label: "排错", task: "检查当前服务异常并定位原因", icon: "alert" },
  { label: "服务", task: "检查当前运行服务和监听端口", icon: "activity" },
];
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
export const isBusy = (phase: TaskPhase) =>
  phase === "connecting" || phase === "running" || phase === "stopping";
const formatDuration = (seconds: number) =>
  seconds < 60
    ? `${seconds}秒`
    : `${Math.floor(seconds / 60)}分${String(seconds % 60).padStart(2, "0")}秒`;

export function AgentPanel({
  onPhaseChange,
  version,
}: {
  onPhaseChange?: (phase: TaskPhase) => void;
  version?: string;
}) {
  const [status, setStatus] = useState<LLMStatus | null>(null);
  const [statusError, setStatusError] = useState("");
  const [task, setTask] = useState("");
  const [record, setRecord] = useState<TaskRecord | null>(null);
  const [tasks, setTasks] = useState<TaskSummary[]>([]);
  const [connectionState, setConnectionState] = useState<"connecting" | "connected" | "disconnected">("connecting");
  const [pendingTitle, setPendingTitle] = useState("");
  const [phase, setPhase] = useState<TaskPhase>("idle");
  const phaseRef = useRef<TaskPhase>("idle");
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [follow, setFollow] = useState(true);
  const followRef = useRef(true);
  const [seconds, setSeconds] = useState(0);
  const startedAt = useRef(0);
  const socketRef = useRef<WebSocket | null>(null);
  const recordRef = useRef<TaskRecord | null>(null);
  const selectedIdRef = useRef<string | null>(null);
  const newTaskModeRef = useRef(false);
  const pendingStartRef = useRef(false);
  const lastSubmittedRef = useRef("");
  const [continuableId, setContinuableId] = useState("");
  const [continueMode, setContinueMode] = useState(false);
  const seenIdsRef = useRef(new Set<string>());
  const retryDelayRef = useRef(300);
  const hasSnapshotRef = useRef(false);
  const logRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  useLayoutEffect(() => {
    const input = inputRef.current;
    if (!input) return;
    input.style.height = "auto";
    input.style.height = `${Math.min(input.scrollHeight, 224)}px`;
  }, [task]);
  const frame = useRef(0);
  const pending = useRef<AgentEvent[]>([]);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout>>();
  const sequence = useRef(0);
  const busy = isBusy(phase);
  const timeline = useMemo(() => buildTimeline(events), [events]);
  const liveActionKey = [...timeline].reverse().find((item) => item.kind === "action" && !item.done && item.error === undefined)?.key;
  const updatePhase = (value: TaskPhase) => {
    phaseRef.current = value;
    setPhase(value);
  };

  const installRecord = (taskRecord: TaskRecord | null, serverState?: AgentSnapshot["state"]) => {
    cancelAnimationFrame(frame.current);
    frame.current = 0;
    pending.current = [];
    pendingStartRef.current = false;
    setPendingTitle("");
    const normalized = taskRecord ? { ...taskRecord, events: taskRecord.events ?? [] } : null;
    recordRef.current = normalized;
    selectedIdRef.current = normalized?.id ?? null;
    setRecord(normalized);
    const received = normalized?.events ?? [];
    seenIdsRef.current = new Set(received.flatMap((event) => event.id === undefined ? [] : [`${event.taskId ?? normalized?.id ?? ""}:${event.id}`]));
    const tail = normalizeEvents(received.map((event) => ({ ...event, taskId: event.taskId ?? normalized?.id }))).slice(-MAX_EVENTS);
    setEvents(tail);
    const nextPhase: TaskPhase = serverState === "running" || serverState === "stopping"
      ? serverState
      : normalized?.phase ?? "idle";
    updatePhase(nextPhase);
    startedAt.current = normalized?.startedAt ?? 0;
    const endedAt = normalized?.endedAt ?? (isBusy(nextPhase) ? Date.now() : normalized?.startedAt ?? Date.now());
    setSeconds(normalized ? Math.max(0, Math.floor((endedAt - normalized.startedAt) / 1000)) : 0);
  };

  const append = (source: AgentEvent) => {
    const taskId = source.taskId ?? recordRef.current?.id;
    if (recordRef.current && taskId && taskId !== recordRef.current.id) return;
    const event = { ...source, taskId, id: source.id ?? ++sequence.current };
    const identity = `${event.taskId ?? ""}:${event.id}`;
    if (seenIdsRef.current.has(identity)) return;
    seenIdsRef.current.add(identity);
    pending.current = mergeEvent(pending.current, event);
    if (frame.current) return;
    frame.current = requestAnimationFrame(() => {
      const batch = pending.current;
      pending.current = [];
      frame.current = 0;
      setEvents((previous) => batch.reduce(mergeEvent, previous));
      const current = recordRef.current;
      if (current && (!event.taskId || event.taskId === current.id)) {
        const updated = { ...current, events: batch.reduce(mergeEvent, current.events).slice(-MAX_EVENTS) };
        recordRef.current = updated;
        setRecord(updated);
      }
    });
  };

  const handleSnapshot = (snapshot: AgentSnapshot) => {
    hasSnapshotRef.current = true;
    const summaries = snapshot.tasks ?? [];
    setTasks(summaries);
    setContinuableId(snapshot.continuable ?? "");
    const incoming = snapshot.task;
    const shouldSelect = snapshot.state !== "idle" || pendingStartRef.current || (selectedIdRef.current === null && !newTaskModeRef.current) || selectedIdRef.current === incoming?.id;
    if (!shouldSelect) return;
    if (incoming) {
      installRecord(incoming, snapshot.state);
    } else if (snapshot.state === "idle" && selectedIdRef.current === null && !newTaskModeRef.current && summaries.length > 0) {
      const latest = summaries.reduce((left, right) => right.startedAt > left.startedAt ? right : left);
      void getTask(latest.id).then((latestRecord) => {
        if (selectedIdRef.current === null && !isBusy(phaseRef.current)) installRecord(latestRecord);
      }).catch(() => installRecord(null, "idle"));
    } else {
      installRecord(null, snapshot.state);
    }
  };

  const loadStatus = () => {
    setStatusError("");
    llmStatus()
      .then(setStatus)
      .catch((error) => setStatusError(`模型配置读取失败：${String(error)}`));
  };
  useEffect(() => {
    let active = true;
    llmStatus()
      .then((value) => {
        if (active) setStatus(value);
      })
      .catch((error) => {
        if (active) setStatusError(`模型配置读取失败：${String(error)}`);
      });
    return () => { active = false; };
  }, []);
  useEffect(() => {
    let active = true;
    const connect = () => {
      if (!active || socketRef.current?.readyState === WebSocket.OPEN || socketRef.current?.readyState === WebSocket.CONNECTING) return;
      setConnectionState("connecting");
      const socket = new WebSocket(wsURL("/ws/agent"));
      socketRef.current = socket;
      const timeout = setTimeout(() => { if (socket.readyState !== WebSocket.OPEN) socket.close(); }, 12_000);
      socket.onopen = () => {
        if (!active || socketRef.current !== socket) return;
        clearTimeout(timeout);
        retryDelayRef.current = 300;
        setConnectionState("connected");
      };
      socket.onmessage = (message) => {
        if (!active || socketRef.current !== socket) return;
        try {
          const value = JSON.parse(message.data);
          if (value?.type === "snapshot") { handleSnapshot(value as AgentSnapshot); return; }
          if (typeof value?.type !== "string") return;
          const event = value as AgentEvent;
          // A start rejection arrives as an error with no task record installed.
          // Surface the reason in the composer and restore the typed draft
          // instead of silently clearing it and hiding an empty log.
          if (event.type === "error" && pendingStartRef.current && !recordRef.current) {
            pendingStartRef.current = false;
            setPendingTitle("");
            updatePhase("idle");
            setStatusError(event.text || "任务未能启动，请稍后重试。");
            setTask((current) => current || lastSubmittedRef.current);
            return;
          }
          if (event.type === "run_state") {
            const next = applyAgentEvent(phaseRef.current, event);
            updatePhase(next);
            if (event.state === "idle") {
              pendingStartRef.current = false;
              setPendingTitle("");
            }
            const current = recordRef.current;
            if (current && (event.state === "running" || event.state === "stopping" || event.state === "idle")) {
              const recordPhase: TaskRecord["phase"] = event.state === "idle" ? "finished" : event.state === "stopping" ? "stopping" : "running";
              const updated: TaskRecord = { ...current, phase: recordPhase, ...(event.state === "idle" ? { endedAt: Date.now() } : {}) };
              recordRef.current = updated;
              setRecord(updated);
            }
            return;
          }
          append(event);
        } catch {
          append({ type: "error", text: "收到无法识别的后台消息。" });
        }
      };
      socket.onerror = () => socket.close();
      socket.onclose = (event) => {
        clearTimeout(timeout);
        if (!active || socketRef.current !== socket) return;
        socketRef.current = null;
        setConnectionState("disconnected");
        // An expired/revoked session will keep rejecting the upgrade; stop
        // retrying and hand off to the pairing gate instead of looping forever.
        if (event.code === SESSION_EXPIRED_CODE) {
          notifySessionExpired();
          return;
        }
        const delay = retryDelayRef.current;
        retryDelayRef.current = Math.min(5_000, delay * 2);
        reconnectTimer.current = setTimeout(connect, delay);
      };
    };
    void listTasks().then((items) => { if (active && !hasSnapshotRef.current) setTasks(items); }).catch(() => {});
    connect();
    return () => {
      active = false;
      clearTimeout(reconnectTimer.current);
      cancelAnimationFrame(frame.current);
      const socket = socketRef.current;
      socketRef.current = null;
      if (socket) { socket.onopen = null; socket.onmessage = null; socket.onclose = null; socket.onerror = null; socket.close(); }
    };
  }, []);
  useEffect(() => {
    onPhaseChange?.(phase);
  }, [phase, onPhaseChange]);
  // Only the shown task can be continued, and only while it is the server's
  // continuable one; reset the default whenever that changes.
  const canContinue = !isBusy(phase) && !!record && record.id === continuableId;
  useEffect(() => {
    setContinueMode(defaultContinue(record?.events ?? [], canContinue));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [record?.id, canContinue]);
  useEffect(() => {
    if (!busy) return;
    const timer = setInterval(
      () => setSeconds(Math.floor((Date.now() - startedAt.current) / 1000)),
      1000,
    );
    return () => clearInterval(timer);
  }, [busy]);
  useEffect(() => {
    if (events.length > 0 && followRef.current && logRef.current)
      logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [events, follow]);

  const setFollowing = (value: boolean) => {
    followRef.current = value;
    setFollow(value);
  };
  const clearLog = () => {
    cancelAnimationFrame(frame.current);
    frame.current = 0;
    pending.current = [];
    setEvents([]);
    setSeconds(0);
    setFollowing(true);
  };

  const start = () => {
    const text = task.trim();
    const socket = socketRef.current;
    if (!text || !status?.hasKey || isBusy(phaseRef.current) || pendingStartRef.current || socket?.readyState !== WebSocket.OPEN) return;
    try {
      setStatusError("");
      lastSubmittedRef.current = text;
      pendingStartRef.current = true;
      newTaskModeRef.current = false;
      const continueFrom = continueMode && canContinue && recordRef.current ? recordRef.current.id : undefined;
      socket.send(JSON.stringify(continueFrom ? { type: "start", task: text, continueFrom } : { type: "start", task: text }));
      selectedIdRef.current = null;
      recordRef.current = null;
      setRecord(null);
      setEvents([]);
      pending.current = [];
      setPendingTitle(text);
      setTask("");
      startedAt.current = Date.now();
      setSeconds(0);
      updatePhase("connecting");
    } catch {
      pendingStartRef.current = false;
      setStatusError("任务发送失败，草稿已保留。请等待连接恢复后重试。");
    }
  };
  const stop = () => {
    if (socketRef.current?.readyState === WebSocket.OPEN && isBusy(phaseRef.current)) {
      try {
        socketRef.current.send(JSON.stringify({ type: "stop" }));
        updatePhase("stopping");
      } catch {
        setStatusError("停止请求未发送，后台连接正在恢复。");
      }
    }
  };
  const newTask = () => {
    if (isBusy(phaseRef.current)) return;
    newTaskModeRef.current = true;
    clearLog();
    recordRef.current = null;
    selectedIdRef.current = null;
    setRecord(null);
    setPendingTitle("");
    setTask("");
    updatePhase("idle");
    requestAnimationFrame(() => inputRef.current?.focus());
  };
  const selectTask = async (id: string) => {
    if (isBusy(phaseRef.current)) return;
    if (!id) { newTask(); return; }
    try {
      const selected = await getTask(id);
      if (isBusy(phaseRef.current)) return;
      newTaskModeRef.current = false;
      installRecord(selected);
      setTask("");
    } catch (error) {
      setStatusError(`任务记录读取失败：${String(error)}`);
    }
  };
  const truncated =
    events.length >= MAX_EVENTS ||
    events.some((event) => event.text?.startsWith(TRUNCATED));
  const empty = !record && !pendingTitle;
  const submittedTask = record?.title ?? pendingTitle;
  const activity = [...events].reverse().find((event) => event.type === "activity");
  const activityLabel = activity?.text ?? ({ preparing: "收集环境", generating: "等待模型", executing: "执行工具" } as Record<string, string>)[activity?.state ?? ""];
  const label = connectionState !== "connected"
    ? connectionState === "connecting" ? "连接后台中" : "后台断开，正在重连"
    : phase === "stopping" ? "等待清理" : phase === "connecting" ? "连接中" : phase === "idle" && status && !status.hasKey ? "未配置模型" : busy ? activityLabel ?? "进行中" : phase === "disconnected" ? "连接断开" : phaseLabels[phase];

  const composer = (
    <div className="composer">
      {statusError && (
        <div className="feedback error composer-alert" role="alert">
          {statusError}{" "}
          <button className="text-button" onClick={loadStatus}>
            重试
          </button>
        </div>
      )}
      <label className="sr-only" htmlFor="task-input">
        部署任务
      </label>
      <textarea
        id="task-input"
        ref={inputRef}
        value={task}
        onChange={(event) => setTask(event.target.value)}
        placeholder={busy ? "准备下一条任务" : empty ? "输入部署任务" : continueMode && canContinue ? "补充说明，在此任务的进展上继续" : "输入下一个任务"}
        rows={2}
        onKeyDown={(event) => {
          // Some IMEs report keyCode 229 after composition ends; that Enter selects text.
          if (
            event.key === "Enter" &&
            !busy &&
            !!status?.hasKey &&
            !event.shiftKey &&
            !event.nativeEvent.isComposing &&
            event.nativeEvent.keyCode !== 229
          ) {
            event.preventDefault();
            start();
          }
        }}
      />
      <div className="composer-foot">
        <Settings status={status} disabled={busy} onSaved={setStatus} version={version} />
        {canContinue && (
          <label className="continue-toggle" title="保留该任务的执行上下文，模型从已有进展继续；关闭则作为独立的新任务">
            <input type="checkbox" checked={continueMode} onChange={(event) => setContinueMode(event.target.checked)} />
            <span>在此任务上继续</span>
          </label>
        )}
        <button
          className="button primary"
          onClick={start}
          disabled={busy || connectionState !== "connected" || !status?.hasKey || !task.trim()}
          title={busy ? "当前任务结束后可执行" : connectionState !== "connected" ? "等待后台连接恢复" : !status?.hasKey ? "请先配置模型" : "Enter 发送，Shift + Enter 换行"}
        >
          执行任务
        </button>
      </div>
    </div>
  );

  return (
    <section
      className="panel agent-panel"
      data-empty={empty}
      id="agent-panel"
      aria-labelledby="agent-title"
    >
      <header className="panel-head">
        <div className="panel-title">
          <h2 id="agent-title" title={empty ? "" : submittedTask}>{empty ? "部署任务" : submittedTask}</h2>
        </div>
        <div className="panel-meta">
          <span className="status-chip">
            <span className="dot" data-tone={phaseTone[phase]} />
            <span role="status">{label}</span>
          </span>
          {tasks.length > 0 && <select className="task-history-select" aria-label="任务历史" value={record?.id ?? ""} disabled={busy} onChange={(event) => void selectTask(event.target.value)}><option value="">任务历史</option>{tasks.map((item) => <option key={item.id} value={item.id}>{item.continuedFrom ? "（续）" : ""}{item.title} | {new Date(item.startedAt).toLocaleString()}</option>)}</select>}
          {seconds > 0 && (
            <span className="meta-extra">{formatDuration(seconds)}</span>
          )}
          {!busy && !empty && (
            <button className="button ghost" onClick={newTask}>
              <Icon name="plus" size={14} />
              新建任务
            </button>
          )}
          {busy && <button className="button danger stop-compact" aria-label={phase === "stopping" ? "正在停止…" : "停止任务"} onClick={stop} disabled={phase === "stopping" || connectionState !== "connected"}><Icon name="stop" size={14} />{phase === "stopping" ? "正在停止" : "停止"}</button>}
        </div>
      </header>
      <div className="agent-main" hidden={empty}>
        <div
          className="agent-scroll"
          ref={logRef}
          onScroll={() => {
            const element = logRef.current;
            if (element)
              setFollowing(
                element.scrollHeight -
                  element.scrollTop -
                  element.clientHeight <
                  48,
              );
          }}
        >
          {!empty && (
            <div className="log">
              <ol className="timeline">
                <li className="tl tl-task">
                  <span className="tl-marker">
                    <Icon name="user" size={14} />
                  </span>
                  <div className="tl-body">
                    <p className="task-text">{submittedTask}</p>
                  </div>
                </li>
                {truncated && (
                  <li className="tl tl-note">
                    <span className="tl-marker">
                      <span className="dot" data-tone="warn" />
                    </span>
                    <div className="tl-body log-notice">较早记录已截断。</div>
                  </li>
                )}
                {timeline.map((item) => (
                  <TimelineRow
                    key={item.key}
                    item={item}
                    live={busy && item.kind === "action" && item.key === liveActionKey}
                  />
                ))}
              </ol>
            </div>
          )}
        </div>
        {!follow && events.length > 0 && (
          <button
            className="button secondary follow-button"
            onClick={() => setFollowing(true)}
          >
            <Icon name="arrow" size={14} />
            返回最新记录
          </button>
        )}
      </div>
      {/* Keep one composer mounted across task states so focus and settings survive. */}
      <div className={empty ? "dock hero" : "dock"}>
        {empty && <h3>开始部署</h3>}
        {composer}
        {empty && (
              <div className="examples" aria-label="示例任务">
                {examples.map((example) => (
                  <button
                    key={example.label}
                    className="example"
                    onClick={() => {
                      setTask(example.task);
                      inputRef.current?.focus();
                    }}
                  >
                    <Icon name={example.icon} size={20} />
                    <span>{example.label}</span>
                    <Icon name="diagonal" size={16} />
                  </button>
                ))}
              </div>
        )}
      </div>
    </section>
  );
}

function TimelineRow({ item, live }: { item: TimelineItem; live: boolean }) {
  switch (item.kind) {
    case "action":
      return <ActionRow item={item} live={live} />;
    // The header owns the current activity; old waiting states are not log entries.
    case "activity":
      return null;
    case "note":
      if (!item.event.text) return null;
      return (
        <li className="tl tl-note">
          <span className="tl-marker">
            <span className="dot" />
          </span>
          <div className="tl-body">{item.event.text}</div>
        </li>
      );
    case "message": {
      const text = item.event.text?.trim();
      return text ? <li className="assistant-message">{text}</li> : null;
    }
    case "error":
      return (
        <li className="tl tl-error">
          <span className="tl-marker">
            <Icon name="alert" size={14} />
          </span>
          <div className="tl-body" role="alert">
            {item.event.text}
          </div>
        </li>
      );
    case "final": {
      const failed = isFailedFinal(item.event);
      return (
        <li className={failed ? "tl tl-final tl-error" : "tl tl-final"}>
          <span className="tl-marker">
            <Icon name={failed ? "alert" : "file"} size={14} />
          </span>
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
  // Live output stays open until the user chooses otherwise.
  const open = expanded ?? (live || item.error !== undefined);
  useEffect(() => {
    if (open && live && outputRef.current)
      outputRef.current.scrollTop = outputRef.current.scrollHeight;
  }, [output, open, live]);

  return (
    <li className="tl tl-action" data-tone={state.tone}>
      <details className="action" open={expanded ?? (live || item.error !== undefined)}>
        <summary className="action-head" onClick={(event) => { event.preventDefault(); setExpanded(!open); }}>
          <Icon name={toolIcons[tool] ?? "terminal"} size={16} />
          <span className="action-label">{toolLabels[tool] ?? (tool || "命令输出")}</span>
          <span className="chip" data-tone={state.tone}>{state.label}</span>
          {item.done?.durationMs !== undefined && <span className="action-duration">{item.done.durationMs} ms</span>}
          <Icon name="chevron" size={14} />
        </summary>
        <div className="action-body">
          {(item.call?.hostname || item.call?.cwd) && <div className="action-evidence">{item.call.hostname && <span>{item.call.hostname}</span>}{item.call.cwd && <span>启动目录 {item.call.cwd}</span>}</div>}
          {target && <code className="action-target">{target}</code>}
          {output && (
            <div className="action-output" data-open={open}>
              <pre ref={outputRef}>{output}</pre>
            </div>
          )}
          {item.error !== undefined && item.error !== "" && <p className="action-error">{item.error}</p>}
        </div>
      </details>
    </li>
  );
}
