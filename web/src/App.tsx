import { useEffect, useState } from "react";
import {
  checkSession,
  clearHash,
  health,
  pair,
  readTokenFromHash,
  type Health,
} from "./api";
import { Terminal } from "./Terminal";
import { AgentPanel } from "./Agent";

type AuthState =
  | { kind: "loading" }
  | { kind: "authed" }
  | { kind: "unauthed" }
  | { kind: "error"; message: string };

export function App() {
  const [auth, setAuth] = useState<AuthState>({ kind: "loading" });
  const [info, setInfo] = useState<Health | null>(null);

  useEffect(() => {
    (async () => {
      const token = readTokenFromHash();
      try {
        if (token) {
          await pair(token);
          clearHash();
          setAuth({ kind: "authed" });
        } else if (await checkSession()) {
          setAuth({ kind: "authed" });
        } else {
          setAuth({ kind: "unauthed" });
        }
      } catch (e) {
        setAuth({ kind: "error", message: String(e) });
      }
    })();
  }, []);

  useEffect(() => {
    health().then(setInfo).catch(() => void 0);
  }, []);

  return (
    <div className="app">
      <div className="topbar">
        <span className="brand">
          Deploy<span className="dot">Pilot</span>
        </span>
        <span className="pill">{info ? `v${info.version.replace(/^v/, "")}` : "…"}</span>
        <span className="pill">{renderAuthPill(auth)}</span>
      </div>
      {auth.kind === "authed" ? (
        <div className="workspace">
          <section className="pane term-pane">
            <header className="pane-head">远程终端</header>
            <Terminal />
          </section>
          <AgentPanel />
        </div>
      ) : (
        <div className="center">
          <div className="card">{renderBody(auth)}</div>
        </div>
      )}
    </div>
  );
}

function renderAuthPill(auth: AuthState): string {
  switch (auth.kind) {
    case "authed":
      return "已接入";
    case "unauthed":
      return "未授权";
    case "error":
      return "错误";
    default:
      return "…";
  }
}

function renderBody(auth: AuthState) {
  switch (auth.kind) {
    case "loading":
      return <p className="muted">正在校验会话…</p>;
    case "unauthed":
      return (
        <>
          <h2>需要配对</h2>
          <p className="muted">
            请在目标机器的终端扫描二维码打开本页面，链接中会带上一次性配对令牌。
          </p>
        </>
      );
    case "error":
      return (
        <>
          <h2 style={{ color: "var(--danger)" }}>配对失败</h2>
          <p className="muted">{auth.message}</p>
          <p className="muted">令牌可能已过期或被使用，请在终端重新生成。</p>
        </>
      );
    default:
      return null;
  }
}
