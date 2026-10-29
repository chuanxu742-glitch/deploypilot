// Minimal API client for the control plane.

export interface Health {
  status: string;
  version: string;
}

/** Reads a one-time pairing token from the URL fragment (#t=...). */
export function readTokenFromHash(): string | null {
  const hash = window.location.hash.replace(/^#/, "");
  const params = new URLSearchParams(hash);
  return params.get("t");
}

/** Removes the token from the address bar without reloading. */
export function clearHash(): void {
  history.replaceState(null, "", window.location.pathname + window.location.search);
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
export async function checkSession(): Promise<boolean> {
  const res = await fetch("/api/session");
  return res.ok;
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
  const res = await fetch("/api/llm/status");
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export async function setLLMConfig(cfg: {
  apiKey?: string;
  model?: string;
}): Promise<LLMStatus> {
  const res = await fetch("/api/llm/config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(cfg),
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export async function listModels(): Promise<string[]> {
  const res = await fetch("/api/llm/models");
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `HTTP ${res.status}`);
  }
  const body = await res.json();
  return body.models ?? [];
}
