import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from "react";
import { authenticate, machineInfo, onSessionExpired, type MachineInfo } from "./api";
import { Terminal, type TerminalConnection } from "./Terminal";
import { AgentPanel, isBusy, phaseTone } from "./Agent";
import type { TaskPhase } from "./agent-state";
import { Icon } from "./Icon";
import { clampSplitRatio, readPreference, readSplitRatio, resolveTheme, writePreference, type ThemeName } from "./ui-preferences";

type AuthState =
  | { kind: "loading" }
  | { kind: "authed" }
  | { kind: "unauthed" }
  | { kind: "error"; message: string };

export function App() {
  const [auth, setAuth] = useState<AuthState>({ kind: "loading" });
  // Only narrow screens switch views; desktop always shows both panels.
  const [view, setView] = useState<"task" | "terminal">("task");
  const [taskPhase, setTaskPhase] = useState<TaskPhase>("idle");
  const [terminal, setTerminal] = useState<TerminalConnection>("connecting");
  const [machine, setMachine] = useState<MachineInfo | null>(null);
  const [theme, setTheme] = useState<ThemeName>(() => resolveTheme(readPreference("deploypilot:theme")));
  const appearanceRef = useRef<HTMLDialogElement>(null);
  const machineRef = useRef<HTMLDetailsElement>(null);
  const [split, setSplit] = useState(() => readSplitRatio(readPreference("deploypilot:split")));
  const workspaceRef = useRef<HTMLElement>(null);
  const authed = auth.kind === "authed";

  useEffect(() => {
    const dismiss = (event: PointerEvent) => {
      if (event.target instanceof Node && machineRef.current && !machineRef.current.contains(event.target)) machineRef.current.open = false;
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && machineRef.current?.open) {
        machineRef.current.open = false;
        machineRef.current.querySelector("summary")?.focus();
      }
    };
    document.addEventListener("pointerdown", dismiss);
    document.addEventListener("keydown", escape);
    return () => { document.removeEventListener("pointerdown", dismiss); document.removeEventListener("keydown", escape); };
  }, []);

  useEffect(() => {
    let active = true;
    authenticate()
      .then((ok) => {
        if (active) setAuth({ kind: ok ? "authed" : "unauthed" });
      })
      .catch((error) => {
        if (active) setAuth({ kind: "error", message: String(error) });
      });
    return () => {
      active = false;
    };
  }, []);

  // When the server reports the session has expired (a 4401 WebSocket close),
  // drop back to the pairing gate so the operator can scan the fresh QR the
  // client reprints, instead of staring at an endless "reconnecting" state.
  useEffect(() => {
    onSessionExpired(() => setAuth({ kind: "unauthed" }));
    return () => onSessionExpired(null);
  }, []);

  useLayoutEffect(() => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.className = theme === "night" ? "dark" : "";
    writePreference("deploypilot:theme", theme);
  }, [theme]);
  useEffect(() => {
    writePreference("deploypilot:split", String(split));
  }, [split]);
  useEffect(() => {
    if (!authed) return;
    let active = true;
    machineInfo().then((info) => { if (active) setMachine(info); }).catch(() => { if (active) setMachine(null); });
    return () => { active = false; };
  }, [auth.kind]);

  const resizeSplit = (event: ReactPointerEvent<HTMLDivElement>) => {
    const bounds = workspaceRef.current?.getBoundingClientRect();
    if (!bounds) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    const style = getComputedStyle(workspaceRef.current!);
    const usableWidth = bounds.width - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight) - 8;
    const update = (clientX: number) => setSplit(clampSplitRatio((clientX - bounds.left - parseFloat(style.paddingLeft)) / usableWidth, usableWidth));
    update(event.clientX);
    const move = (moveEvent: PointerEvent) => update(moveEvent.clientX);
    const end = () => { window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", end); window.removeEventListener("pointercancel", end); };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", end, { once: true });
    window.addEventListener("pointercancel", end, { once: true });
  };

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <h1 className="brand-name">DeployPilot</h1>
        </div>
        {authed && (
          <nav className="view-switch" aria-label="切换面板">
            <button
              aria-pressed={view === "task"}
              aria-controls="agent-panel"
              onClick={() => setView("task")}
            >
              <Icon name="spark" size={14} />
              部署任务
              {isBusy(taskPhase) || taskPhase === "disconnected" ? (
                <span className="dot" data-tone={phaseTone[taskPhase]} />
              ) : null}
            </button>
            <button
              aria-pressed={view === "terminal"}
              aria-controls="terminal-panel"
              onClick={() => setView("terminal")}
            >
              <Icon name="terminal" size={14} />
              远程终端
              {terminal === "disconnected" && (
                <span className="dot" data-tone="bad" />
              )}
            </button>
          </nav>
        )}
        {authed && machine && <details ref={machineRef} className="machine-details"><summary><Icon name="system" size={16} /><span>{machine.hostname}</span><Icon name="chevron" size={14} /></summary><div><span>系统 {machine.platform}</span><span>用户 {machine.username}</span><span>启动目录 {machine.cwd}</span></div></details>}
        {authed ? <>
        <button className="icon-button theme-control" aria-label="外观设置" title="外观设置" onClick={() => appearanceRef.current?.showModal()}><Icon name="settings" size={18} /></button>
        <dialog className="settings-dialog appearance-dialog" ref={appearanceRef} aria-labelledby="appearance-title" onClick={(event) => { if (event.target === event.currentTarget) event.currentTarget.close(); }}>
          <div className="dialog-content">
            <div className="dialog-head"><h2 id="appearance-title">外观</h2><button className="icon-button" aria-label="关闭外观设置" onClick={() => appearanceRef.current?.close()}><Icon name="close" size={18} /></button></div>
            <div className="theme-options" role="group" aria-label="外观">
              {([['day', '日间'], ['night', '夜间'], ['comfort', '护眼']] as const).map(([value, label]) => <button key={value} className="theme-option" aria-pressed={theme === value} onClick={() => setTheme(value)}><span>{label}</span>{theme === value && <Icon name="check" size={16} />}</button>)}
            </div>
          </div>
        </dialog>
        </> : <span className="status-chip session">
          <span
            className="dot"
            data-tone={
              authed ? "ok" : auth.kind === "error" ? "bad" : ""
            }
          />
          {renderAuthPill(auth)}
        </span>}
      </header>
      {authed ? (
        <main ref={workspaceRef} className="workspace" data-view={view} style={{ "--terminal-ratio": `${split}fr`, "--task-ratio": `${1 - split}fr` } as CSSProperties}>
          {/* Both panels stay mounted so switching views keeps the task and the shell. */}
          <section
            className="panel term-panel dark"
            id="terminal-panel"
            aria-label="远程终端"
          >
            <Terminal theme={theme} taskActive={isBusy(taskPhase)} onConnectionChange={setTerminal} />
          </section>
          <div className="splitter" role="separator" aria-label="调整面板宽度" aria-orientation="vertical" aria-valuemin={40} aria-valuemax={75} aria-valuenow={Math.round(split * 100)} tabIndex={0} onPointerDown={resizeSplit} onKeyDown={(event) => { if (event.key === "ArrowRight") { event.preventDefault(); setSplit((value) => clampSplitRatio(value + 0.02)); } else if (event.key === "ArrowLeft") { event.preventDefault(); setSplit((value) => clampSplitRatio(value - 0.02)); } }} />
          <AgentPanel version={machine?.version} onPhaseChange={setTaskPhase} />
        </main>
      ) : (
        <div className="gate">
          <div className="gate-card">{renderGate(auth)}</div>
        </div>
      )}
    </div>
  );
}

function renderAuthPill(auth: AuthState): string {
  switch (auth.kind) {
    case "authed":
      return "已配对";
    case "unauthed":
      return "未配对";
    case "error":
      return "配对失败";
    default:
      return "校验中";
  }
}

function renderGate(auth: AuthState) {
  switch (auth.kind) {
    case "loading":
      return <p className="muted">正在校验会话…</p>;
    case "unauthed":
      return (
        <>
          <span className="gate-mark">
            <Icon name="link" size={20} />
          </span>
          <h2>连接目标机器</h2>
          <ol className="gate-steps">
            <li>
              <span className="step-num">1</span>
              在目标机器上运行 DeployPilot
            </li>
            <li>
              <span className="step-num">2</span>
              扫描终端里的二维码，或打开完整的配对链接
            </li>
          </ol>
        </>
      );
    case "error":
      return (
        <>
          <span className="gate-mark" data-tone="bad">
            <Icon name="alert" size={20} />
          </span>
          <h2>配对失败</h2>
          <p className="gate-error">{auth.message}</p>
          <p className="muted">
            配对链接只能使用一次，10 分钟内有效。请在目标机器重启
            DeployPilot，获取新的链接。
          </p>
        </>
      );
    default:
      return null;
  }
}
