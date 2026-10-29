export interface AgentEvent {
  id?: number;
  taskId?: string;
  type: string;
  text?: string;
  tool?: string;
  command?: string;
  path?: string;
  exitCode?: number;
  success?: boolean;
  step?: number;
  state?: string;
  timestamp?: number;
  durationMs?: number;
  hostname?: string;
  cwd?: string;
  callId?: string;
}

export type TaskPhase =
  "idle" | "connecting" | "running" | "stopping" | "finished" | "interrupted" | "disconnected";

/** A final event the model reported as an explicit failure (success === false). */
export function isFailedFinal(event: AgentEvent): boolean {
  return event.type === "final" && event.success === false;
}
export const MAX_EVENTS = 300;
export const MAX_EVENT_TEXT = 32_768;
export const TRUNCATED = "…（较早内容已截断）\n";

export function applyAgentEvent(
  phase: TaskPhase,
  event: AgentEvent,
): TaskPhase {
  // Only the server's cleanup acknowledgement releases the execution controls.
  if (event.type !== "run_state") return phase;
  if (event.state === "running" || event.state === "stopping")
    return event.state;
  return event.state === "idle" ? "finished" : phase;
}

export function mergeEvent(
  previous: AgentEvent[],
  event: AgentEvent,
): AgentEvent[] {
  if (event.id !== undefined && previous.some((item) => item.id === event.id && item.taskId === event.taskId))
    return previous;
  const clip = (text: string) =>
    text.length > MAX_EVENT_TEXT
      ? TRUNCATED + text.slice(-(MAX_EVENT_TEXT - TRUNCATED.length))
      : text;
  const bounded =
    event.text === undefined ? event : { ...event, text: clip(event.text) };
  const last = previous[previous.length - 1];
  if (
    (event.type === "thinking" || event.type === "output") &&
    last?.type === event.type
  ) {
    return [
      ...previous.slice(0, -1),
      { ...last, text: clip((last.text ?? "") + (event.text ?? "")) },
    ].slice(-MAX_EVENTS);
  }
  return [...previous, bounded].slice(-MAX_EVENTS);
}

export interface ActionItem {
  kind: "action";
  key: number;
  call?: AgentEvent;
  output?: string;
  done?: AgentEvent;
  error?: string;
}
export type TimelineItem =
  | { kind: "activity" | "note" | "message" | "error" | "final"; key: number; event: AgentEvent }
  | ActionItem;

/**
 * Groups the flat event stream into timeline items. A tool call owns the output,
 * completion and immediate error (e.g. a blocked command) that follow it.
 * Output or completion without a preceding call (the call was trimmed by
 * MAX_EVENTS) becomes its own action so nothing is dropped.
 */
export function buildTimeline(events: AgentEvent[]): TimelineItem[] {
  const items: TimelineItem[] = [];
  let open: ActionItem | null = null;
  for (let index = 0; index < events.length; index++) {
    const event = events[index];
    const key = event.id ?? index;
    if (event.type === "activity") {
      for (let i = items.length - 1; i >= 0; i--) {
        if (items[i].kind === "activity") items.splice(i, 1);
      }
      items.push({ kind: "activity", key, event });
    } else if (event.type === "tool_call") {
      open = { kind: "action", key, call: event };
      items.push(open);
    } else if (event.type === "output") {
      if (event.callId) {
        open = [...items].reverse().find((item): item is ActionItem => item.kind === "action" && item.call?.callId === event.callId) ?? null;
      }
      if (!open || open.done || open.error !== undefined) {
        open = { kind: "action", key };
        items.push(open);
      }
      open.output = (open.output ?? "") + (event.text ?? "");
    } else if (event.type === "tool_done") {
      if (event.callId) {
        open = [...items].reverse().find((item): item is ActionItem => item.kind === "action" && item.call?.callId === event.callId) ?? null;
      }
      if (!open || open.done) {
        open = { kind: "action", key };
        items.push(open);
      }
      open.done = event;
    } else if (event.type === "error") {
      const target = event.callId
        ? [...items].reverse().find((item): item is ActionItem => item.kind === "action" && item.call?.callId === event.callId)
        : open && !open.done && open.error === undefined ? open : undefined;
      if (target) target.error = event.text ?? "";
      else { open = null; items.push({ kind: "error", key, event }); }
    } else if (event.type === "thinking") {
      // Historical wire name: this is public assistant content, not hidden reasoning.
      items.push({ kind: "message", key, event });
    } else {
      open = null;
      if (event.type === "status") items.push({ kind: "note", key, event });
      else if (event.type === "error") items.push({ kind: "error", key, event });
      else if (event.type === "final") items.push({ kind: "final", key, event });
    }
  }
  return items;
}
