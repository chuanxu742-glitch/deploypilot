import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  checkSessionAfterDisconnect,
  getTask,
  listTasks,
  llmStatus,
  notifySessionExpired,
  wsURL,
  SESSION_EXPIRED_CODE,
  type AgentSnapshot,
  type LLMStatus,
  type TaskRecord,
  type TaskSummary,
} from "./api";
import {
  applyAgentEvent,
  buildTimeline,
  mergeEvent,
  normalizeEvents,
  MAX_EVENTS,
  TRUNCATED,
  type AgentEvent,
  type TaskPhase,
} from "./agent-state";
import { Icon, type IconName } from "./Icon";
import { Settings } from "./Settings";
import { SessionSidebar, shortTaskTitle } from "./SessionSidebar";
import { TimelineList } from "./Timeline";
import "./agent-interactions.css";

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
  {
    label: "环境",
    task: "检查目标机器的系统版本、磁盘、内存和 GPU 环境",
    icon: "system",
  },
  { label: "部署项目", task: "帮我检查当前项目并完成部署", icon: "box" },
  { label: "排错", task: "检查当前服务异常并定位原因", icon: "alert" },
  { label: "服务", task: "检查当前运行服务和监听端口", icon: "activity" },
];
export const isBusy = (phase: TaskPhase) =>
  phase === "connecting" || phase === "running" || phase === "stopping";
const formatDuration = (seconds: number) =>
  seconds < 60
    ? `${seconds}秒`
    : `${Math.floor(seconds / 60)}分${String(seconds % 60).padStart(2, "0")}秒`;

function ContextTaskTitle({ text }: { text: string }) {
  return (
    <span className="composer-context-title" title={text}>
      {text}
    </span>
  );
}

type RecoverySubmission = {
  originKey: string;
  requestId: string;
  // Preserve submission identity separately from the operator's editable recovery copy.
  submittedText: string;
  submittedDraft: string;
  alternateDraft: string;
  hasAlternate: boolean;
  displayed: "submitted" | "alternate";
  autoRestored: boolean;
  reason: "uncertain" | "rejected";
};

type SteerSubmission = {
  taskId: string;
  requestId: string;
  text: string;
  state:
    "sending" | "uncertain" | "queued" | "applied" | "deferred" | "rejected";
  message?: string;
};

export function AgentPanel({
  onPhaseChange,
  version,
  sidebarTarget,
  onCloseSidebar,
  onShowTask,
}: {
  onPhaseChange?: (phase: TaskPhase) => void;
  version?: string;
  sidebarTarget: HTMLDivElement | null;
  onCloseSidebar: () => void;
  onShowTask: () => void;
}) {
  const [status, setStatus] = useState<LLMStatus | null>(null);
  const [statusError, setStatusError] = useState("");
  const [task, setTask] = useState("");
  const taskValueRef = useRef("");
  const draftsRef = useRef(new Map<string, string>());
  const [record, setRecord] = useState<TaskRecord | null>(null);
  const [tasks, setTasks] = useState<TaskSummary[]>([]);
  const [connectionState, setConnectionState] = useState<
    "connecting" | "connected" | "disconnected"
  >("connecting");
  const [snapshotReady, setSnapshotReady] = useState(false);
  const [pendingTitle, setPendingTitle] = useState("");
  const [phase, setPhase] = useState<TaskPhase>("idle");
  const [globalBusy, setGlobalBusy] = useState(false);
  const [globalRunState, setGlobalRunState] = useState<
    "idle" | "running" | "stopping"
  >("idle");
  const [globalTaskTitle, setGlobalTaskTitle] = useState("");
  const [globalTaskId, setGlobalTaskId] = useState<string | null>(null);
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
  const historyRequestRef = useRef<{
    sequence: number;
    controller: AbortController | null;
  }>({ sequence: 0, controller: null });
  const pendingStartRef = useRef<{
    requestId: string;
    text: string;
    continueFrom?: string;
  } | null>(null);
  const activeTaskIdRef = useRef<string | null>(null);
  const globalRunStateRef = useRef<AgentSnapshot["state"]>("idle");
  const steerSubmissionsRef = useRef(new Map<string, SteerSubmission>());
  const [steerSubmissions, setSteerSubmissions] = useState<SteerSubmission[]>(
    [],
  );
  const [recoverySubmission, setRecoverySubmission] =
    useState<RecoverySubmission | null>(null);
  const recoverySubmissionRef = useRef<RecoverySubmission | null>(null);
  const retryActionRef = useRef<(() => void) | null>(null);
  const [continuableId, setContinuableId] = useState("");
  const seenIdsRef = useRef(new Set<string>());
  const retryDelayRef = useRef(300);
  const hasSnapshotRef = useRef(false);
  const socketSnapshotReadyRef = useRef(false);
  const logRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const input = inputRef.current;
    if (!input) return;
    input.style.height = "auto";
    input.style.height = `${Math.min(input.scrollHeight, 224)}px`;
  }, [task]);
  const frame = useRef(0);
  const pending = useRef<AgentEvent[]>([]);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout>>();
  const sequence = useRef(0);
  const busy = globalBusy || isBusy(phase);
  const timeline = useMemo(() => buildTimeline(events), [events]);
  const visibleUserRequestIds = useMemo(
    () =>
      new Set(
        events.flatMap((event) =>
          event.type === "user" && event.requestId ? [event.requestId] : [],
        ),
      ),
    [events],
  );
  const liveActionKey = useMemo(
    () =>
      [...timeline]
        .reverse()
        .find(
          (item) =>
            item.kind === "action" && !item.done && item.error === undefined,
        )?.key,
    [timeline],
  );
  const updatePhase = (value: TaskPhase) => {
    phaseRef.current = value;
    setPhase(value);
  };
  const draftKey = () => selectedIdRef.current ?? "__new__";
  const updateRecoverySubmission = (value: RecoverySubmission | null) => {
    recoverySubmissionRef.current = value;
    setRecoverySubmission(value);
  };
  const changeTask = (value: string, source: "user" | "system" = "system") => {
    taskValueRef.current = value;
    draftsRef.current.set(draftKey(), value);
    if (
      source === "user" &&
      recoverySubmissionRef.current?.originKey === draftKey()
    ) {
      updateRecoverySubmission({
        ...recoverySubmissionRef.current,
        ...(recoverySubmissionRef.current.displayed === "submitted"
          ? { submittedDraft: value }
          : { alternateDraft: value }),
        autoRestored: false,
      });
    }
    setTask(value);
  };
  const showStatusError = (text: string, retry: (() => void) | null = null) => {
    retryActionRef.current = retry;
    setStatusError(text);
  };
  const updateSteer = (submission: SteerSubmission) => {
    const key = `${submission.taskId}:${submission.requestId}`;
    const previous = steerSubmissionsRef.current.get(key);
    // Applied is final for this request, including when an older history GET arrives later.
    const state =
      previous?.state === "applied"
        ? "applied"
        : submission.state === "queued" && previous?.state === "deferred"
          ? "deferred"
          : submission.state;
    steerSubmissionsRef.current.set(key, { ...previous, ...submission, state });
    setSteerSubmissions([...steerSubmissionsRef.current.values()]);
  };
  const consumeSteerEvent = (event: AgentEvent) => {
    if (!event.taskId || !event.requestId) return false;
    const previous = steerSubmissionsRef.current.get(
      `${event.taskId}:${event.requestId}`,
    );
    let state: SteerSubmission["state"] | undefined;
    if (
      event.type === "steer_ack" ||
      (event.type === "user" && event.state === "queued")
    )
      state = "queued";
    else if (event.type === "activity" && event.state === "steer_applied")
      state = "applied";
    else if (event.type === "activity" && event.state === "steer_deferred")
      state = "deferred";
    else if (event.type === "error" && previous) state = "rejected";
    if (!state) return false;
    updateSteer({
      taskId: event.taskId,
      requestId: event.requestId,
      text: event.type === "user" ? (event.text ?? "") : (previous?.text ?? ""),
      state,
      message: event.type === "error" ? event.text : undefined,
    });
    return true;
  };
  // Unconfirmed text stays bound to its originating session; it is never replayed automatically.
  const confirmPendingStart = (
    requestId: string,
    acceptedRecord?: TaskRecord | null,
    acceptedSummary?: TaskSummary,
  ) => {
    const pendingStart = pendingStartRef.current;
    const recovery = recoverySubmissionRef.current;
    const acceptedId = acceptedRecord?.id ?? acceptedSummary?.id;
    const originKey =
      pendingStart?.continueFrom ?? recovery?.originKey ?? "__new__";
    const isOriginSelected = draftKey() === originKey;
    if (pendingStart?.requestId === requestId) {
      pendingStartRef.current = null;
      setPendingTitle("");
      const originDraft = isOriginSelected
        ? taskValueRef.current
        : (draftsRef.current.get(originKey) ?? "");
      if (originDraft && acceptedId)
        draftsRef.current.set(acceptedId, originDraft);
      if (!pendingStart.continueFrom) {
        draftsRef.current.delete("__new__");
        if (acceptedId && isOriginSelected) {
          selectedIdRef.current = acceptedId;
          newTaskModeRef.current = false;
        }
      }
    }
    if (recovery?.requestId === requestId) {
      const stillViewingOrigin = draftKey() === recovery.originKey;
      if (
        recovery.autoRestored &&
        stillViewingOrigin &&
        taskValueRef.current === recovery.submittedText
      )
        changeTask("");
      const originDraft = draftsRef.current.get(recovery.originKey) ?? "";
      if (recovery.originKey === "__new__" && acceptedId) {
        if (originDraft) draftsRef.current.set(acceptedId, originDraft);
        draftsRef.current.delete("__new__");
        if (stillViewingOrigin) {
          selectedIdRef.current = acceptedId;
          newTaskModeRef.current = false;
        }
      }
      updateRecoverySubmission(null);
    }
    setStatusError((current) =>
      current.includes("提交状态不确定") ? "" : current,
    );
  };
  const restorePendingStart = (
    pendingStart: NonNullable<typeof pendingStartRef.current>,
    reason: RecoverySubmission["reason"],
    rejectionText?: string,
  ) => {
    pendingStartRef.current = null;
    retryActionRef.current = null;
    setPendingTitle("");
    if (!pendingStart.continueFrom) {
      selectedIdRef.current = null;
      newTaskModeRef.current = true;
      recordRef.current = null;
      setRecord(null);
      setEvents([]);
    }
    updatePhase(recordRef.current?.phase ?? "idle");
    const existingDraft = taskValueRef.current;
    const originKey = pendingStart.continueFrom ?? "__new__";
    if (!existingDraft) {
      changeTask(pendingStart.text);
    }
    updateRecoverySubmission({
      originKey,
      requestId: pendingStart.requestId,
      submittedText: pendingStart.text,
      submittedDraft: pendingStart.text,
      hasAlternate: !!existingDraft && existingDraft !== pendingStart.text,
      alternateDraft:
        existingDraft && existingDraft !== pendingStart.text
          ? existingDraft
          : "",
      displayed:
        existingDraft && existingDraft !== pendingStart.text
          ? "alternate"
          : "submitted",
      autoRestored: !existingDraft,
      reason,
    });
    showStatusError(
      rejectionText ??
        "任务提交状态不确定，已保留文本；请核对任务历史后再手动决定，系统不会自动重发。",
    );
  };

  const installRecord = (
    taskRecord: TaskRecord | null,
    serverState?: AgentSnapshot["state"],
  ) => {
    cancelAnimationFrame(frame.current);
    frame.current = 0;
    pending.current = [];
    const normalized = taskRecord
      ? { ...taskRecord, events: taskRecord.events ?? [] }
      : null;
    for (const event of normalized?.events ?? [])
      consumeSteerEvent({ ...event, taskId: event.taskId ?? normalized?.id });
    recordRef.current = normalized;
    selectedIdRef.current = normalized?.id ?? null;
    activeTaskIdRef.current =
      normalized && (serverState === "running" || serverState === "stopping")
        ? normalized.id
        : activeTaskIdRef.current;
    setRecord(normalized);
    const received = normalized?.events ?? [];
    seenIdsRef.current = new Set(
      received.flatMap((event) =>
        event.id === undefined
          ? []
          : [`${event.taskId ?? normalized?.id ?? ""}:${event.id}`],
      ),
    );
    const tail = normalizeEvents(
      received.map((event) => ({
        ...event,
        taskId: event.taskId ?? normalized?.id,
      })),
    ).slice(-MAX_EVENTS);
    setEvents(tail);
    const nextPhase: TaskPhase =
      serverState === "running" || serverState === "stopping"
        ? serverState
        : (normalized?.phase ?? "idle");
    updatePhase(nextPhase);
    startedAt.current = normalized?.startedAt ?? 0;
    const endedAt =
      normalized?.endedAt ??
      (isBusy(nextPhase) ? Date.now() : (normalized?.startedAt ?? Date.now()));
    setSeconds(
      normalized
        ? Math.max(0, Math.floor((endedAt - normalized.startedAt) / 1000))
        : 0,
    );
  };

  const append = (source: AgentEvent) => {
    const taskId = source.taskId ?? recordRef.current?.id;
    if (taskId && taskId !== recordRef.current?.id) return;
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
        const updated = {
          ...current,
          events: batch.reduce(mergeEvent, current.events).slice(-MAX_EVENTS),
        };
        recordRef.current = updated;
        setRecord(updated);
      }
    });
  };

  const handleSnapshot = (snapshot: AgentSnapshot) => {
    hasSnapshotRef.current = true;
    setSnapshotReady(true);
    const summaries = snapshot.tasks ?? [];
    setTasks(summaries);
    setContinuableId(snapshot.continuable ?? "");
    const incoming = snapshot.task;
    globalRunStateRef.current = snapshot.state;
    for (const event of incoming?.events ?? [])
      consumeSteerEvent({ ...event, taskId: event.taskId ?? incoming?.id });
    const selectedSummary = summaries.find(
      (summary) => summary.id === selectedIdRef.current,
    );
    if (
      selectedSummary &&
      selectedIdRef.current !== incoming?.id &&
      recordRef.current
    ) {
      recordRef.current = {
        ...recordRef.current,
        phase: selectedSummary.phase,
        canContinue: selectedSummary.canContinue,
        endedAt: selectedSummary.endedAt,
      };
      setRecord(recordRef.current);
      if (!pendingStartRef.current) updatePhase(selectedSummary.phase);
    }
    setGlobalTaskTitle(
      snapshot.state === "idle" ? "" : (incoming?.title ?? ""),
    );
    setGlobalTaskId(snapshot.state === "idle" ? null : (incoming?.id ?? null));
    // Global execution controls follow the snapshot, while the selected record stays user-owned.
    setGlobalBusy(
      snapshot.state === "running" || snapshot.state === "stopping",
    );
    setGlobalRunState(snapshot.state);
    activeTaskIdRef.current =
      snapshot.state === "idle" ? null : (incoming?.id ?? null);
    const matchingRequestId =
      pendingStartRef.current?.requestId ??
      recoverySubmissionRef.current?.requestId;
    const acceptedSummary =
      matchingRequestId &&
      summaries.find((summary) => summary.requestId === matchingRequestId);
    // A matching request ID confirms acceptance; a different running task is not a rejection.
    if (matchingRequestId && incoming?.requestId === matchingRequestId) {
      confirmPendingStart(matchingRequestId, incoming);
    } else if (matchingRequestId && acceptedSummary) {
      confirmPendingStart(matchingRequestId, null, acceptedSummary);
      const sequence = ++historyRequestRef.current.sequence;
      historyRequestRef.current.controller?.abort();
      const controller = new AbortController();
      historyRequestRef.current.controller = controller;
      void getTask(acceptedSummary.id, controller.signal)
        .then((accepted) => {
          if (
            sequence === historyRequestRef.current.sequence &&
            selectedIdRef.current === accepted.id
          )
            installRecord(accepted);
        })
        .catch(() => {});
    } else if (pendingStartRef.current && snapshot.state === "idle") {
      restorePendingStart(pendingStartRef.current, "uncertain");
    }
    const pendingMismatch =
      !!pendingStartRef.current &&
      incoming?.requestId !== pendingStartRef.current.requestId;
    // Keep a selected history or new-session draft visible when another tab owns the active task.
    const shouldSelect =
      !pendingMismatch &&
      ((snapshot.state !== "idle" &&
        selectedIdRef.current === null &&
        !newTaskModeRef.current) ||
        (selectedIdRef.current !== null &&
          selectedIdRef.current === incoming?.id) ||
        (snapshot.state === "idle" &&
          selectedIdRef.current === null &&
          !newTaskModeRef.current));
    if (!shouldSelect) return;
    if (incoming) {
      installRecord(incoming, snapshot.state);
    } else if (
      snapshot.state === "idle" &&
      selectedIdRef.current === null &&
      !newTaskModeRef.current &&
      summaries.length > 0
    ) {
      const latest = summaries.reduce((left, right) =>
        right.startedAt > left.startedAt ? right : left,
      );
      const sequence = ++historyRequestRef.current.sequence;
      void getTask(latest.id)
        .then((latestRecord) => {
          if (
            sequence === historyRequestRef.current.sequence &&
            selectedIdRef.current === null &&
            !isBusy(phaseRef.current)
          )
            installRecord(latestRecord);
        })
        .catch(() => {
          if (
            sequence === historyRequestRef.current.sequence &&
            selectedIdRef.current === null
          )
            installRecord(null, "idle");
        });
    } else {
      installRecord(null, snapshot.state);
    }
  };

  const loadStatus = () => {
    showStatusError("");
    llmStatus()
      .then(setStatus)
      .catch((error) =>
        showStatusError(`模型配置读取失败：${String(error)}`, loadStatus),
      );
  };
  useEffect(() => {
    let active = true;
    llmStatus()
      .then((value) => {
        if (active) setStatus(value);
      })
      .catch((error) => {
        if (active)
          showStatusError(`模型配置读取失败：${String(error)}`, loadStatus);
      });
    return () => {
      active = false;
    };
  }, []);
  useEffect(() => {
    let active = true;
    const connect = () => {
      if (
        !active ||
        socketRef.current?.readyState === WebSocket.OPEN ||
        socketRef.current?.readyState === WebSocket.CONNECTING
      )
        return;
      setConnectionState("connecting");
      const socket = new WebSocket(wsURL("/ws/agent"));
      socketRef.current = socket;
      socketSnapshotReadyRef.current = false;
      setSnapshotReady(false);
      const timeout = setTimeout(() => {
        if (socket.readyState !== WebSocket.OPEN) socket.close();
      }, 12_000);
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
          if (value?.type === "snapshot") {
            socketSnapshotReadyRef.current = true;
            hasSnapshotRef.current = true;
            handleSnapshot(value as AgentSnapshot);
            return;
          }
          if (typeof value?.type !== "string") return;
          const event = value as AgentEvent;
          const isSteerEvent = consumeSteerEvent(event);
          if (
            event.type === "steer_ack" ||
            (isSteerEvent && event.type === "error")
          )
            return;
          // A request-scoped rejection keeps both the submitted text and any newer draft available.
          if (
            event.type === "error" &&
            pendingStartRef.current &&
            !event.taskId &&
            (!event.requestId ||
              event.requestId === pendingStartRef.current.requestId)
          ) {
            restorePendingStart(
              pendingStartRef.current,
              "rejected",
              event.text || "任务未能启动，请稍后重试。",
            );
            return;
          }
          if (event.type === "run_state") {
            globalRunStateRef.current =
              event.state === "running" || event.state === "stopping"
                ? event.state
                : "idle";
            const matchingSelectedTask = !event.taskId
              ? !!activeTaskIdRef.current &&
                activeTaskIdRef.current === recordRef.current?.id
              : event.taskId === recordRef.current?.id;
            setGlobalBusy(
              event.state === "running" || event.state === "stopping",
            );
            setGlobalRunState(
              event.state === "running" || event.state === "stopping"
                ? event.state
                : "idle",
            );
            if (event.state === "idle") setGlobalTaskId(null);
            else if (event.taskId) setGlobalTaskId(event.taskId);
            if (matchingSelectedTask) {
              const next = applyAgentEvent(phaseRef.current, event);
              updatePhase(next);
            }
            if (event.state === "idle" && matchingSelectedTask) {
              setPendingTitle("");
            }
            const current = recordRef.current;
            if (
              current &&
              matchingSelectedTask &&
              (event.state === "running" ||
                event.state === "stopping" ||
                event.state === "idle")
            ) {
              const recordPhase: TaskRecord["phase"] =
                event.state === "idle"
                  ? "finished"
                  : event.state === "stopping"
                    ? "stopping"
                    : "running";
              const updated: TaskRecord = {
                ...current,
                phase: recordPhase,
                ...(event.state === "idle" ? { endedAt: Date.now() } : {}),
              };
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
        for (const submission of steerSubmissionsRef.current.values()) {
          if (submission.state === "sending")
            updateSteer({ ...submission, state: "uncertain" });
        }
        // An expired/revoked session will keep rejecting the upgrade; stop
        // retrying and hand off to the pairing gate instead of looping forever.
        if (event.code === SESSION_EXPIRED_CODE) {
          notifySessionExpired();
          return;
        }
        void checkSessionAfterDisconnect().then((valid) => {
          if (!valid || !active) return;
        });
        // A close after a transmitted start leaves its identity and text pending.
        // The next connection's first snapshot decides whether it was accepted.
        const delay = retryDelayRef.current;
        retryDelayRef.current = Math.min(5_000, delay * 2);
        reconnectTimer.current = setTimeout(connect, delay);
      };
    };
    void listTasks()
      .then((items) => {
        if (active && !hasSnapshotRef.current) setTasks(items);
      })
      .catch(() => {});
    connect();
    return () => {
      active = false;
      clearTimeout(reconnectTimer.current);
      cancelAnimationFrame(frame.current);
      const socket = socketRef.current;
      socketRef.current = null;
      if (socket) {
        socket.onopen = null;
        socket.onmessage = null;
        socket.onclose = null;
        socket.onerror = null;
        socket.close();
      }
    };
  }, []);
  useEffect(() => {
    onPhaseChange?.(
      globalBusy
        ? globalRunState === "stopping"
          ? "stopping"
          : "running"
        : phase,
    );
  }, [phase, globalBusy, globalRunState, onPhaseChange]);
  // Like a chat: a message sent while the server's continuable session is shown
  // stays in that session. "新建任务" clears the view to start a fresh one.
  const selectedCapability =
    tasks.find((item) => item.id === record?.id)?.canContinue ??
    record?.canContinue;
  const canContinue =
    !isBusy(phase) &&
    !!record &&
    (selectedCapability ?? record.id === continuableId);
  const selectedActive = globalBusy && !!record && record.id === globalTaskId;
  const steerPending = steerSubmissions.some(
    (item) => item.taskId === record?.id && item.state === "sending",
  );
  const canSendSteer =
    selectedActive && globalRunState === "running" && !steerPending;
  const continuationUnavailable = !!record && !isBusy(phase) && !canContinue;
  const selectedSteerSubmissions = steerSubmissions.filter(
    (item) => item.taskId === record?.id,
  );
  useEffect(() => {
    if (!isBusy(phase)) return;
    const timer = setInterval(
      () => setSeconds(Math.floor((Date.now() - startedAt.current) / 1000)),
      1000,
    );
    return () => clearInterval(timer);
  }, [phase]);
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

  const sendSteer = () => {
    const text = taskValueRef.current.trim();
    const taskId = recordRef.current?.id;
    const socket = socketRef.current;
    if (
      !text ||
      !taskId ||
      taskId !== activeTaskIdRef.current ||
      globalRunStateRef.current !== "running" ||
      socket?.readyState !== WebSocket.OPEN ||
      !socketSnapshotReadyRef.current ||
      [...steerSubmissionsRef.current.values()].some(
        (item) => item.taskId === taskId && item.state === "sending",
      )
    )
      return;
    const requestId =
      globalThis.crypto?.randomUUID?.() ??
      `${Date.now()}-${Math.random().toString(16).slice(2)}`;
    updateSteer({ taskId, requestId, text, state: "sending" });
    try {
      socket.send(
        JSON.stringify({ type: "steer", taskId, task: text, requestId }),
      );
      changeTask("");
    } catch {
      updateSteer({
        taskId,
        requestId,
        text,
        state: "rejected",
        message: "补充未发送，草稿已保留。",
      });
    }
  };
  const start = () => {
    const text = task.trim();
    const socket = socketRef.current;
    if (
      !text ||
      !status?.hasKey ||
      globalBusy ||
      continuationUnavailable ||
      isBusy(phaseRef.current) ||
      pendingStartRef.current ||
      socket?.readyState !== WebSocket.OPEN ||
      !socketSnapshotReadyRef.current
    )
      return;
    const submittedDraftKey = draftKey();
    try {
      historyRequestRef.current.sequence++;
      historyRequestRef.current.controller?.abort();
      historyRequestRef.current.controller = null;
      showStatusError("");
      const requestId =
        globalThis.crypto?.randomUUID?.() ??
        `${Date.now()}-${Math.random().toString(16).slice(2)}`;
      const continueFrom =
        canContinue && recordRef.current ? recordRef.current.id : undefined;
      pendingStartRef.current = { requestId, text, continueFrom };
      newTaskModeRef.current = false;
      socket.send(
        JSON.stringify({
          type: "start",
          task: text,
          requestId,
          ...(continueFrom ? { continueFrom } : {}),
        }),
      );
      if (!continueFrom) {
        // A new session: clear the view until the server's snapshot arrives.
        selectedIdRef.current = null;
        recordRef.current = null;
        setRecord(null);
        setEvents([]);
        pending.current = [];
        setPendingTitle(text);
      }
      if (
        recoverySubmissionRef.current?.originKey === submittedDraftKey &&
        recoverySubmissionRef.current.submittedText === text
      )
        updateRecoverySubmission(null);
      // Selection may already point at the new task; clear the submitted source draft.
      draftsRef.current.set(submittedDraftKey, "");
      taskValueRef.current = "";
      setTask("");
      startedAt.current = Date.now();
      setSeconds(0);
      updatePhase("connecting");
    } catch {
      const pendingStart = pendingStartRef.current;
      pendingStartRef.current = null;
      if (pendingStart && !taskValueRef.current) changeTask(pendingStart.text);
      showStatusError("任务发送失败，草稿已保留。请等待连接恢复后重试。");
    }
  };
  const stop = () => {
    if (
      socketRef.current?.readyState === WebSocket.OPEN &&
      (globalBusy || isBusy(phaseRef.current))
    ) {
      try {
        socketRef.current.send(JSON.stringify({ type: "stop" }));
        setGlobalRunState("stopping");
        // Global stop must not change the phase of an unrelated selected history.
        if (
          !recordRef.current ||
          recordRef.current.id === activeTaskIdRef.current
        )
          updatePhase("stopping");
      } catch {
        showStatusError("停止请求未发送，后台连接正在恢复。");
      }
    }
  };
  const newTask = () => {
    if (globalBusy || isBusy(phaseRef.current)) return;
    historyRequestRef.current.sequence++;
    historyRequestRef.current.controller?.abort();
    historyRequestRef.current.controller = null;
    showStatusError("");
    newTaskModeRef.current = true;
    clearLog();
    recordRef.current = null;
    selectedIdRef.current = null;
    setRecord(null);
    setPendingTitle("");
    selectedIdRef.current = null;
    changeTask(draftsRef.current.get("__new__") ?? "");
    updatePhase("idle");
    requestAnimationFrame(() => inputRef.current?.focus());
  };
  const selectTask = async (id: string) => {
    if (pendingStartRef.current) return;
    if (!id) {
      newTask();
      return;
    }
    const sequence = ++historyRequestRef.current.sequence;
    historyRequestRef.current.controller?.abort();
    const controller = new AbortController();
    historyRequestRef.current.controller = controller;
    showStatusError("");
    try {
      const selected = await getTask(id, controller.signal);
      if (sequence !== historyRequestRef.current.sequence) return;
      draftsRef.current.set(draftKey(), taskValueRef.current);
      newTaskModeRef.current = false;
      installRecord(
        selected,
        selected.id === activeTaskIdRef.current
          ? globalRunStateRef.current
          : undefined,
      );
      changeTask(draftsRef.current.get(id) ?? "");
    } catch (error) {
      if (
        sequence === historyRequestRef.current.sequence &&
        !controller.signal.aborted
      )
        showStatusError(`任务记录读取失败：${String(error)}`, () => {
          void selectTask(id);
        });
    }
  };
  const truncated = useMemo(
    () =>
      events.length >= MAX_EVENTS ||
      events.some((event) => event.text?.startsWith(TRUNCATED)),
    [events],
  );
  const empty = !record && !pendingTitle;
  const heroEmpty = empty && !globalBusy;
  const hideHeader =
    empty && !globalBusy && tasks.length === 0 && !recoverySubmission;
  const submittedTask = record?.title ?? pendingTitle;
  const fullHeaderTitle = heroEmpty
    ? "部署任务"
    : submittedTask ||
      (globalBusy ? globalTaskTitle || "后台任务" : "部署任务");
  const headerTitle = shortTaskTitle(fullHeaderTitle);
  const otherGlobalTask =
    globalBusy &&
    !!record &&
    (globalTaskId
      ? globalTaskId !== record.id
      : !!globalTaskTitle && globalTaskTitle !== submittedTask);
  const activity = useMemo(
    () => [...events].reverse().find((event) => event.type === "activity"),
    [events],
  );
  const activityLabel =
    activity?.text ??
    (
      {
        preparing: "收集环境",
        generating: "等待模型",
        executing: "执行工具",
      } as Record<string, string>
    )[activity?.state ?? ""];
  const label =
    connectionState !== "connected"
      ? connectionState === "connecting"
        ? "连接后台中"
        : "后台断开，正在重连"
      : globalBusy && !selectedActive
        ? "后台任务仍在执行，当前查看历史"
        : globalBusy && globalRunState === "stopping"
          ? "等待后台清理"
          : globalBusy
            ? (activityLabel ?? "后台任务执行中")
            : phase === "stopping"
              ? "等待清理"
              : phase === "connecting"
                ? "连接中"
                : phase === "idle" && status && !status.hasKey
                  ? "未配置模型"
                  : phase === "disconnected"
                    ? "连接断开"
                    : phaseLabels[phase];

  const composer = (
    <div className="composer">
      {statusError && (
        <div className="feedback error composer-alert" role="alert">
          {statusError}{" "}
          {retryActionRef.current && (
            <button
              className="text-button"
              onClick={() => retryActionRef.current?.()}
            >
              重试
            </button>
          )}
        </div>
      )}
      <label className="sr-only" htmlFor="task-input">
        部署任务
      </label>
      <textarea
        id="task-input"
        ref={inputRef}
        value={task}
        onChange={(event) => changeTask(event.target.value, "user")}
        placeholder={
          selectedActive
            ? "补充当前任务，点击发送后在当前执行步骤结束后应用"
            : globalBusy
              ? "编辑所选会话的草稿，后台任务结束后可发送"
              : empty
                ? "输入部署任务"
                : canContinue
                  ? "继续当前任务（从会话栏新建独立会话）"
                  : "此会话暂不能继续，请新建会话"
        }
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
      {busy && (
        <div className="composer-context" role="status">
          {!record ? (
            <>
              后台任务仍在运行：
              <ContextTaskTitle text={globalTaskTitle || "其他会话任务"} />
              这份草稿仍属于新会话，不会发给后台任务。
            </>
          ) : otherGlobalTask ? (
            <>
              后台任务仍在运行：
              <ContextTaskTitle text={globalTaskTitle || "其他会话任务"} />
              这份草稿仍属于所选会话：
              <ContextTaskTitle text={submittedTask || "当前会话"} />
              不会发给后台任务。
            </>
          ) : (
            <>
              补充发送后等待当前执行步骤结束，再交给模型应用；不会打断当前命令。
            </>
          )}
          要开始独立会话，请等待结束后从会话栏新建。
        </div>
      )}
      {!globalBusy && continuationUnavailable && (
        <div className="composer-context" role="status">
          此会话无法继续，记录仍可查看。请从会话栏新建会话后发送独立任务；此处草稿会保留。
        </div>
      )}
      {selectedSteerSubmissions.length > 0 && (
        <div className="steer-status-list" aria-label="补充消息状态">
          {selectedSteerSubmissions.map((submission) => (
            <div
              className="steer-status"
              data-steer-request={submission.requestId}
              key={submission.requestId}
              role="status"
            >
              <span>
                {submission.state === "sending"
                  ? "正在确认补充是否接收"
                  : submission.state === "queued"
                    ? "补充已接收，当前执行步骤结束后应用"
                    : submission.state === "applied"
                      ? "补充已应用到模型上下文"
                      : submission.state === "deferred"
                        ? "补充尚未应用，手动继续此任务后处理"
                        : submission.state === "rejected"
                          ? (submission.message ?? "补充未接收")
                          : "补充接收状态未确认，不会自动重发"}
              </span>
              {submission.text &&
                !["uncertain", "rejected", "deferred"].includes(
                  submission.state,
                ) &&
                !visibleUserRequestIds.has(submission.requestId) && (
                  <details className="steer-message-content">
                    <summary>查看补充内容</summary>
                    <pre>{submission.text}</pre>
                  </details>
                )}
              {(submission.state === "uncertain" ||
                submission.state === "rejected" ||
                submission.state === "deferred") &&
                submission.text && (
                  <>
                    <pre>{submission.text}</pre>
                    {submission.state !== "deferred" && (
                      <button
                        className="text-button"
                        onClick={() => {
                          const draft = taskValueRef.current;
                          changeTask(
                            draft
                              ? `${draft}\n${submission.text}`
                              : submission.text,
                            "user",
                          );
                          inputRef.current?.focus();
                        }}
                      >
                        {task ? "追加到草稿" : "恢复补充草稿"}
                      </button>
                    )}
                    {submission.state === "deferred" && (
                      <span>
                        补充已保存在此会话，继续任务时处理，无需再次发送。
                      </span>
                    )}
                  </>
                )}
            </div>
          ))}
        </div>
      )}
      {recoverySubmission?.originKey === draftKey() && (
        <div className="feedback error" role="status">
          {recoverySubmission.reason === "uncertain"
            ? "提交是否已被后台接受仍不确定"
            : "后台明确拒绝了这次提交"}
          ：
          {recoverySubmission.displayed === "submitted"
            ? recoverySubmission.submittedDraft
            : recoverySubmission.alternateDraft}
          {recoverySubmission.hasAlternate && (
            <button
              className="text-button"
              onClick={() => {
                const nextDisplayed =
                  recoverySubmission.displayed === "submitted"
                    ? "alternate"
                    : "submitted";
                changeTask(
                  nextDisplayed === "submitted"
                    ? recoverySubmission.submittedDraft
                    : recoverySubmission.alternateDraft,
                );
                updateRecoverySubmission({
                  ...recoverySubmission,
                  displayed: nextDisplayed,
                });
              }}
            >
              {recoverySubmission.displayed === "submitted"
                ? "切回当前草稿"
                : "载入待确认文本"}
            </button>
          )}
        </div>
      )}
      <div className="composer-foot">
        <Settings
          status={status}
          disabled={busy}
          onSaved={setStatus}
          version={version}
        />
        <button
          className="button primary"
          onClick={selectedActive ? sendSteer : start}
          disabled={
            (selectedActive
              ? !canSendSteer
              : busy || continuationUnavailable) ||
            connectionState !== "connected" ||
            !snapshotReady ||
            !status?.hasKey ||
            !task.trim()
          }
          title={
            selectedActive
              ? globalRunState === "stopping"
                ? "停止清理期间不能发送补充"
                : "当前执行步骤结束后应用补充，点击才会发送"
              : busy
                ? "请返回正在执行的会话发送补充"
                : continuationUnavailable
                  ? "此会话无法继续，请新建会话"
                  : connectionState !== "connected"
                    ? "等待后台连接恢复"
                    : !snapshotReady
                      ? "等待后台任务状态确认"
                      : !status?.hasKey
                        ? "请先配置模型"
                        : "Enter 发送，Shift + Enter 换行"
          }
        >
          {selectedActive ? "发送补充" : "执行任务"}
        </button>
      </div>
    </div>
  );
  const closeMobileSidebar = () => {
    if (window.matchMedia("(max-width: 820px)").matches) {
      onShowTask();
      onCloseSidebar();
    }
  };
  const selectTaskRef = useRef(selectTask);
  const newTaskRef = useRef(newTask);
  const closeMobileSidebarRef = useRef(closeMobileSidebar);
  const closeSidebarRef = useRef(onCloseSidebar);
  selectTaskRef.current = selectTask;
  newTaskRef.current = newTask;
  closeMobileSidebarRef.current = closeMobileSidebar;
  closeSidebarRef.current = onCloseSidebar;
  const onSidebarSelect = useCallback((id: string) => {
    void selectTaskRef.current(id);
    closeMobileSidebarRef.current();
  }, []);
  const onSidebarNew = useCallback(() => {
    newTaskRef.current();
    closeMobileSidebarRef.current();
  }, []);
  const onSidebarClose = useCallback(() => closeSidebarRef.current(), []);

  return (
    <>
      <section
        className="panel agent-panel"
        data-empty={hideHeader}
        id="agent-panel"
        aria-labelledby="agent-title"
      >
        <header className="panel-head">
          <div className="panel-title">
            <Icon name="spark" size={16} />
            <h2 id="agent-title" title={heroEmpty ? "" : fullHeaderTitle}>
              {headerTitle}
            </h2>
          </div>
        </header>
        {globalBusy && !selectedActive && (
          <div className="active-session-banner">
            <span>
              后台任务：{shortTaskTitle(globalTaskTitle || "正在执行的会话")}
            </span>
            <button
              className="text-button"
              onClick={() => {
                if (globalTaskId) void selectTask(globalTaskId);
              }}
            >
              返回正在执行的会话
            </button>
          </div>
        )}
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
                <TimelineList
                  items={timeline}
                  submittedTask={submittedTask}
                  truncated={truncated}
                  busy={selectedActive}
                  liveActionKey={liveActionKey}
                />
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
        <div className={heroEmpty ? "dock hero" : "dock"}>
          {heroEmpty && <h3>开始部署</h3>}
          <div
            className="run-status"
            data-quiet={
              heroEmpty && phase === "idle" && label === phaseLabels.idle
            }
          >
            <span className="status-chip">
              <span className="dot" data-tone={phaseTone[phase]} />
              <span role="status">{label}</span>
            </span>
            {seconds > 0 && (
              <span className="meta-extra">{formatDuration(seconds)}</span>
            )}
            {busy && (
              <button
                className="button danger stop-compact"
                aria-label={
                  globalRunState === "stopping" || phase === "stopping"
                    ? "正在停止…"
                    : "停止任务"
                }
                onClick={stop}
                disabled={
                  globalRunState === "stopping" ||
                  phase === "stopping" ||
                  connectionState !== "connected"
                }
              >
                <Icon name="stop" size={14} />
                {globalRunState === "stopping" || phase === "stopping"
                  ? "正在停止"
                  : "停止"}
              </button>
            )}
          </div>
          {composer}
          {heroEmpty && (
            <div className="examples" aria-label="示例任务">
              {examples.map((example) => (
                <button
                  key={example.label}
                  className="example"
                  onClick={() => {
                    changeTask(example.task, "user");
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
      {sidebarTarget &&
        createPortal(
          <SessionSidebar
            tasks={tasks}
            selectedId={record?.id ?? null}
            busy={busy}
            globalBusy={globalBusy}
            globalTaskId={globalTaskId}
            globalTaskTitle={globalTaskTitle}
            globalRunState={globalRunState}
            onSelect={onSidebarSelect}
            onNew={onSidebarNew}
            onClose={onSidebarClose}
          />,
          sidebarTarget,
        )}
    </>
  );
}
