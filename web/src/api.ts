// Minimal API client for the control plane.

// The server closes an expired/revoked WebSocket with this application close
// code (see internal/server/session.go) so the UI can tell expiry apart from a
// transient drop and return to the pairing gate instead of retrying forever.
export const SESSION_EXPIRED_CODE = 4401;

// WebSocket reconnect timing shared by the task and terminal sockets.
export const RECONNECT_INITIAL_MS = 300;
const RECONNECT_MAX_MS = 5_000;
/** A socket that has not opened by now is closed and retried. */
export const SOCKET_OPEN_TIMEOUT_MS = 12_000;
export const nextReconnectDelay = (delay: number) =>
  Math.min(RECONNECT_MAX_MS, delay * 2);

/** A unique ID for one submission, so a retry is never applied twice. */
export const createRequestId = () =>
  globalThis.crypto?.randomUUID?.() ??
  `${Date.now()}-${Math.random().toString(16).slice(2)}`;

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

/** An authenticated request whose successful response is JSON. */
async function fetchJSON<T>(
  input: RequestInfo | URL,
  init?: RequestInit,
): Promise<T> {
  const response = await authenticatedFetch(input, init);
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response.json();
}

export function wsURL(path: string): string {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${protocol}//${window.location.host}${path}`;
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

/** Whether the server is running a task, regardless of which one is shown. */
export type RunState = "idle" | "running" | "stopping";

export interface AgentSnapshot {
  type: "snapshot";
  state: RunState;
  task: TaskRecord | null;
  tasks: TaskSummary[];
  /** Compatibility fallback for servers without per-record canContinue. */
  continuable?: string;
}

export function listTasks(signal?: AbortSignal): Promise<TaskSummary[]> {
  return fetchJSON("/api/tasks", { signal });
}

export function getTask(id: string, signal?: AbortSignal): Promise<TaskRecord> {
  return fetchJSON(`/api/tasks/${encodeURIComponent(id)}`, { signal });
}

export function machineInfo(): Promise<MachineInfo> {
  return fetchJSON("/api/machine");
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

async function pair(token: string): Promise<void> {
  const response = await fetch("/api/pair", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token }),
  });
  if (!response.ok) {
    throw new Error(`pair failed: HTTP ${response.status}`);
  }
}

/** Checks whether the current cookie is an authenticated session. */
async function checkSession(): Promise<boolean> {
  const response = await fetch("/api/session");
  if (!response.ok && response.status !== 401)
    throw new Error(`会话校验失败：HTTP ${response.status}`);
  return response.ok;
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

export interface LLMStatus {
  hasKey: boolean;
  model: string;
}

export function llmStatus(): Promise<LLMStatus> {
  return fetchJSON("/api/llm/status");
}

export function setLLMConfig(
  cfg: {
    apiKey?: string;
    model?: string;
  },
  signal?: AbortSignal,
): Promise<LLMStatus> {
  return fetchJSON("/api/llm/config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(cfg),
    signal,
  });
}

export async function listModels(signal?: AbortSignal): Promise<string[]> {
  const response = await authenticatedFetch("/api/llm/models", { signal });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new Error(body.error || `HTTP ${response.status}`);
  }
  const body = await response.json();
  return body.models ?? [];
}
