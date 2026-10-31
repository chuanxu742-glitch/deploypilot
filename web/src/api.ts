// Minimal API client for the control plane.

// The server closes an expired/revoked WebSocket with this application close
// code (see internal/server/session.go) so the UI can tell expiry apart from a
// transient drop and return to the pairing gate instead of retrying forever.
export const SESSION_EXPIRED_CODE = 4401;

let sessionExpiredHandler: (() => void) | null = null;
/** Registers (or clears, with null) the handler run when the session expires. */
export function onSessionExpired(handler: (() => void) | null): void {
  sessionExpiredHandler = handler;
}
/** Called by the WS/HTTP layers on a 4401 close or a post-pairing 401. */
export function notifySessionExpired(): void {
  sessionExpiredHandler?.();
}

async function authenticatedFetch(
  input: RequestInfo | URL,
  init?: RequestInit,
): Promise<Response> {
  const response = await fetch(input, init);
  if (response.status === 401) notifySessionExpired();
  return response;
}

export function wsURL(path: string): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}${path}`;
}

export interface Health {
  status: string;
  version: string;
}

export interface MachineInfo {
  hostname: string;
  platform: string;
  username: string;
  cwd: string;
  version: string;
}

export interface TaskSummary {
  id: string;
  title: string;
  startedAt: number;
  endedAt?: number;
  phase: "running" | "stopping" | "finished" | "interrupted";
  requestId?: string;
  /** Whether this server can restore the conversation behind this record. */
  canContinue?: boolean;
}

export interface TaskRecord extends TaskSummary {
  events: import("./agent-state").AgentEvent[];
}

export interface AgentSnapshot {
  type: "snapshot";
  state: "idle" | "running" | "stopping";
  task: TaskRecord | null;
  tasks: TaskSummary[];
  /** Compatibility fallback for servers without per-record canContinue. */
  continuable?: string;
}

export async function listTasks(signal?: AbortSignal): Promise<TaskSummary[]> {
  const response = await authenticatedFetch("/api/tasks", { signal });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response.json();
}

export async function getTask(
  id: string,
  signal?: AbortSignal,
): Promise<TaskRecord> {
  const response = await authenticatedFetch(
    `/api/tasks/${encodeURIComponent(id)}`,
    { signal },
  );
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response.json();
}

export async function machineInfo(): Promise<MachineInfo> {
  const res = await authenticatedFetch("/api/machine");
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

/** Reads a one-time pairing token from the URL fragment (#t=...). */
function readTokenFromHash(): string | null {
  const hash = window.location.hash.replace(/^#/, "");
  const params = new URLSearchParams(hash);
  return params.get("t");
}

/** Removes the token from the address bar without reloading. */
function clearHash(): void {
  history.replaceState(
    null,
    "",
    window.location.pathname + window.location.search,
  );
}

export async function pair(token: string): Promise<void> {
  const res = await fetch("/api/pair", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token }),
  });
  if (!res.ok) {
    throw new Error(`pair failed: HTTP ${res.status}`);
  }
}

/** Checks whether the current cookie is an authenticated session. */
async function checkSession(): Promise<boolean> {
  const res = await fetch("/api/session");
  if (!res.ok && res.status !== 401)
    throw new Error(`会话校验失败：HTTP ${res.status}`);
  return res.ok;
}

let disconnectSessionCheck: Promise<boolean> | null = null;

/**
 * Returns false only for an HTTP 401, after notifying the app; other responses,
 * network failures, and the four-second timeout remain eligible for reconnect.
 * Concurrent socket failures share one probe.
 */
export async function checkSessionAfterDisconnect(): Promise<boolean> {
  if (!disconnectSessionCheck) {
    disconnectSessionCheck = (async () => {
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), 4_000);
      try {
        const response = await fetch("/api/session", {
          signal: controller.signal,
        });
        if (response.status === 401) {
          notifySessionExpired();
          return false;
        }
        return true;
      } catch {
        return true;
      } finally {
        clearTimeout(timeout);
      }
    })().finally(() => {
      disconnectSessionCheck = null;
    });
  }
  return disconnectSessionCheck;
}

let authentication: Promise<boolean> | null = null;
/** Share in-flight pairing: React development effects must not consume the token twice. */
export function authenticate(): Promise<boolean> {
  if (!authentication) {
    authentication = (async () => {
      const token = readTokenFromHash();
      if (!token) return checkSession();
      if (await checkSession()) {
        clearHash();
        return true;
      }
      try {
        await pair(token);
        clearHash();
        return true;
      } catch (pairError) {
        // The one-time token may already have been consumed by this browser.
        if (await checkSession()) {
          clearHash();
          return true;
        }
        throw pairError;
      }
    })().finally(() => {
      authentication = null;
    });
  }
  return authentication;
}

export async function health(): Promise<Health> {
  const res = await fetch("/api/health");
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export interface LLMStatus {
  hasKey: boolean;
  model: string;
}

export async function llmStatus(): Promise<LLMStatus> {
  const res = await authenticatedFetch("/api/llm/status");
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export async function setLLMConfig(
  cfg: {
    apiKey?: string;
    model?: string;
  },
  signal?: AbortSignal,
): Promise<LLMStatus> {
  const res = await authenticatedFetch("/api/llm/config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(cfg),
    signal,
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export async function listModels(signal?: AbortSignal): Promise<string[]> {
  const res = await authenticatedFetch("/api/llm/models", { signal });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `HTTP ${res.status}`);
  }
  const body = await res.json();
  return body.models ?? [];
}
