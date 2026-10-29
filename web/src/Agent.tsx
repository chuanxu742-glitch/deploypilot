import { useEffect, useRef, useState } from "react";
import { listModels, llmStatus, setLLMConfig, type LLMStatus } from "./api";

interface AgentEvent {
  type: string;
  text?: string;
  tool?: string;
  command?: string;
  path?: string;
  exitCode?: number;
  success?: boolean;
  step?: number;
}

function wsURL(path: string): string {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}${path}`;
}

export function AgentPanel() {
  const [status, setStatus] = useState<LLMStatus | null>(null);
  const [task, setTask] = useState("");
  const [running, setRunning] = useState(false);
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const socketRef = useRef<WebSocket | null>(null);
  const logRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    llmStatus().then(setStatus).catch(() => void 0);
  }, []);

  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight });
  }, [events]);

  const ensureSocket = (): WebSocket => {
    if (socketRef.current && socketRef.current.readyState <= WebSocket.OPEN) {
      return socketRef.current;
    }
    const sock = new WebSocket(wsURL("/ws/agent"));
    sock.onmessage = (ev) => {
      const e = JSON.parse(ev.data) as AgentEvent;
      setEvents((prev) => mergeEvent(prev, e));
      if (e.type === "final" || e.type === "error") setRunning(false);
    };
    sock.onclose = () => setRunning(false);
    socketRef.current = sock;
    return sock;
  };

  const start = () => {
    if (!task.trim()) return;
    setEvents([]);
    setRunning(true);
    const sock = ensureSocket();
    const send = () => sock.send(JSON.stringify({ type: "start", task }));
    if (sock.readyState === WebSocket.OPEN) send();
    else sock.onopen = send;
  };

  const stop = () => {
    socketRef.current?.send(JSON.stringify({ type: "stop" }));
    setRunning(false);
  };

  return (
    <section className="pane agent-pane">
      <header className="pane-head agent-head">
        <span>AI 部署助手</span>
        <Settings status={status} onSaved={setStatus} />
      </header>

      <div className="agent-log" ref={logRef}>
        {events.length === 0 && (
          <p className="muted small">
            输入一个部署任务，例如「部署 Qwen2.5-0.5B 并用 vLLM 起服务」，助手会自动执行。
          </p>
        )}
        {events.map((e, i) => (
          <EventRow key={i} e={e} />
        ))}
      </div>

      <div className="agent-input">
        <textarea
          value={task}
          onChange={(e) => setTask(e.target.value)}
          placeholder="描述你要完成的部署任务…"
          rows={2}
          disabled={running}
        />
        {running ? (
          <button className="btn danger" onClick={stop}>
            停止
          </button>
        ) : (
          <button className="btn" onClick={start} disabled={!status?.hasKey}>
            执行
          </button>
        )}
      </div>
      {!status?.hasKey && (
        <p className="muted small warn">请先在上方设置中填写模型 API Key。</p>
      )}
    </section>
  );
}

// mergeEvent coalesces consecutive "thinking" and "output" deltas into one row.
function mergeEvent(prev: AgentEvent[], e: AgentEvent): AgentEvent[] {
  if ((e.type === "thinking" || e.type === "output") && prev.length > 0) {
    const last = prev[prev.length - 1];
    if (last.type === e.type) {
      const copy = prev.slice();
      copy[copy.length - 1] = { ...last, text: (last.text ?? "") + (e.text ?? "") };
      return copy;
    }
  }
  return [...prev, e];
}

function EventRow({ e }: { e: AgentEvent }) {
  switch (e.type) {
    case "status":
      return <div className="ev ev-status">{e.text}</div>;
    case "thinking":
      return <div className="ev ev-think">{e.text}</div>;
    case "tool_call":
      return (
        <div className="ev ev-tool">
          <span className="tag">{e.tool}</span>
          <code>{e.command ?? e.path}</code>
        </div>
      );
    case "output":
      return <pre className="ev ev-out">{e.text}</pre>;
    case "tool_done":
      return (
        <div className="ev ev-done">
          {e.tool} 完成{typeof e.exitCode === "number" ? ` (exit ${e.exitCode})` : ""}
        </div>
      );
    case "final":
      return (
        <div className={"ev ev-final " + (e.success ? "ok" : "bad")}>
          <strong>{e.success ? "✓ 任务完成" : "任务结束"}</strong>
          <div>{e.text}</div>
        </div>
      );
    case "error":
      return <div className="ev ev-error">⚠ {e.text}</div>;
    default:
      return null;
  }
}

function Settings({
  status,
  onSaved,
}: {
  status: LLMStatus | null;
  onSaved: (s: LLMStatus) => void;
}) {
  const [open, setOpen] = useState(false);
  const [apiKey, setApiKey] = useState("");
  const [model, setModel] = useState(status?.model ?? "");
  const [models, setModels] = useState<string[]>([]);
  const [msg, setMsg] = useState("");

  const save = async () => {
    setMsg("");
    try {
      const cfg: { apiKey?: string; model?: string } = {};
      if (apiKey) cfg.apiKey = apiKey;
      if (model) cfg.model = model;
      const s = await setLLMConfig(cfg);
      onSaved(s);
      setApiKey("");
      setMsg("已保存");
    } catch (e) {
      setMsg(String(e));
    }
  };

  const fetchModels = async () => {
    setMsg("");
    try {
      setModels(await listModels());
    } catch (e) {
      setMsg(String(e));
    }
  };

  return (
    <div className="settings">
      <button className="link" onClick={() => setOpen((v) => !v)}>
        设置 {status?.hasKey ? "●" : "○"}
      </button>
      {open && (
        <div className="settings-pop">
          <label>API Key（仅保存在内存，不写入磁盘）</label>
          <input
            type="password"
            value={apiKey}
            placeholder={status?.hasKey ? "已设置，留空则不修改" : "sk-..."}
            onChange={(e) => setApiKey(e.target.value)}
          />
          <label>模型</label>
          <div className="row">
            <input
              value={model}
              placeholder="模型名"
              onChange={(e) => setModel(e.target.value)}
              list="model-list"
            />
            <button className="link" onClick={fetchModels}>
              拉取列表
            </button>
          </div>
          <datalist id="model-list">
            {models.map((m) => (
              <option key={m} value={m} />
            ))}
          </datalist>
          <div className="row">
            <button className="btn" onClick={save}>
              保存
            </button>
            {msg && <span className="muted small">{msg}</span>}
          </div>
        </div>
      )}
    </div>
  );
}
