import { useEffect, useRef, useState } from "react";
import {
  checkSessionAfterDisconnect,
  notifySessionExpired,
  wsURL,
  SESSION_EXPIRED_CODE,
} from "./api";
import { Icon } from "./Icon";
import { Terminal as XTerm } from "@xterm/xterm";
import type { ITheme } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import {
  slate,
  sand,
  red,
  amber,
  green,
  blue,
  purple,
  cyan,
  slateDark,
  redDark,
  amberDark,
  greenDark,
  blueDark,
  purpleDark,
  cyanDark,
} from "@radix-ui/colors";
import "@xterm/xterm/css/xterm.css";
import type { ThemeName } from "./ui-preferences";

export type TerminalConnection = "connecting" | "connected" | "disconnected";

// Match the surrounding terminal surface in index.css; changing the palette
// updates the existing xterm so scrollback and the shell connection survive.
function terminalPalette(theme: ThemeName): ITheme {
  const neutral =
    theme === "comfort"
      ? {
          surface: amber.amber2,
          raised: sand.sand2,
          muted: sand.sand11,
          soft: sand.sand11,
          text: sand.sand12,
        }
      : theme === "night"
        ? {
            surface: slateDark.slate1,
            raised: slateDark.slate2,
            muted: slateDark.slate9,
            soft: slateDark.slate11,
            text: slateDark.slate12,
          }
        : {
            surface: slate.slate1,
            raised: slate.slate2,
            muted: slate.slate11,
            soft: slate.slate11,
            text: slate.slate12,
          };
  const accents =
    theme === "night"
      ? {
          red: redDark,
          amber: amberDark,
          green: greenDark,
          blue: blueDark,
          purple: purpleDark,
          cyan: cyanDark,
        }
      : { red, amber, green, blue, purple, cyan };
  return {
    background: neutral.surface,
    foreground: neutral.text,
    cursor: accents.blue.blue11,
    cursorAccent: neutral.surface,
    selectionBackground: accents.blue.blue5,
    selectionForeground: neutral.text,
    black: theme === "night" ? neutral.raised : neutral.text,
    brightBlack: neutral.muted,
    white: neutral.soft,
    brightWhite: neutral.text,
    red: accents.red.red11,
    brightRed: accents.red.red12,
    green: accents.green.green11,
    brightGreen: accents.green.green12,
    yellow: accents.amber.amber11,
    brightYellow: accents.amber.amber12,
    blue: accents.blue.blue11,
    brightBlue: accents.blue.blue12,
    magenta: accents.purple.purple11,
    brightMagenta: accents.purple.purple12,
    cyan: accents.cyan.cyan11,
    brightCyan: accents.cyan.cyan12,
  };
}

export function Terminal({
  onConnectionChange,
  theme = "day",
  taskActive = false,
}: {
  onConnectionChange?: (connection: TerminalConnection) => void;
  theme?: ThemeName;
  taskActive?: boolean;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const reconnectRef = useRef<() => void>(() => {});
  const terminalRef = useRef<XTerm | null>(null);
  const taskActiveRef = useRef(taskActive);
  taskActiveRef.current = taskActive;
  const agentStateRef = useRef("unknown");
  const [agentState, setAgentState] = useState("unknown");
  const [connection, setConnection] =
    useState<TerminalConnection>("connecting");
  const [clipboardStatus, setClipboardStatus] = useState("");
  const readOnly = taskActive || agentState !== "idle";

  useEffect(
    () => onConnectionChange?.(connection),
    [connection, onConnectionChange],
  );
  useEffect(() => {
    if (terminalRef.current)
      terminalRef.current.options.disableStdin = readOnly;
  }, [readOnly]);

  useEffect(() => {
    const element = containerRef.current;
    if (!element) return;

    const term = new XTerm({
      fontFamily:
        '"IBM Plex Mono", "Cascadia Code", Menlo, Consolas, monospace',
      fontSize: 14,
      cursorBlink: false,
      disableStdin: true,
      screenReaderMode: true,
      scrollback: 5000,
      theme: terminalPalette(theme),
    });
    terminalRef.current = term;
    const fit = new FitAddon();
    term.loadAddon(fit);

    let socket: WebSocket | null = null;
    let disposed = false;
    let resizeFrame = 0;
    let connectTimer: ReturnType<typeof setTimeout>;
    let retryTimer: ReturnType<typeof setTimeout>;
    let retryDelay = 300;
    let clipboardTimer: ReturnType<typeof setTimeout>;
    const encoder = new TextEncoder();
    const clipboardFeedback = (message: string) => {
      if (disposed) return;
      clearTimeout(clipboardTimer);
      setClipboardStatus(message);
      clipboardTimer = setTimeout(() => setClipboardStatus(""), 1500);
    };

    term.attachCustomKeyEventHandler((event) => {
      if (
        event.key.toLowerCase() !== "c" ||
        event.altKey ||
        !(event.ctrlKey || event.metaKey)
      )
        return true;
      // Unselected Ctrl+C stays a shell interrupt; selected Ctrl+C and Ctrl/Cmd+Shift+C copy.
      if (!term.hasSelection() && !event.shiftKey && !event.metaKey)
        return true;
      event.preventDefault();
      event.stopPropagation();
      if (event.type === "keydown" && term.hasSelection()) {
        const text = term.getSelection();
        let copied = false;
        try {
          copied = document.execCommand("copy");
        } catch {
          /* Use the async clipboard fallback. */
        }
        if (copied) clipboardFeedback("已复制");
        else if (navigator.clipboard?.writeText) {
          void navigator.clipboard.writeText(text).then(
            () => clipboardFeedback("已复制"),
            () => clipboardFeedback("复制失败"),
          );
        } else clipboardFeedback("复制失败");
      }
      return false;
    });

    const sendResize = () => {
      if (socket?.readyState === WebSocket.OPEN)
        socket.send(
          JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }),
        );
    };
    const connect = () => {
      if (
        disposed ||
        socket?.readyState === WebSocket.OPEN ||
        socket?.readyState === WebSocket.CONNECTING
      )
        return;
      agentStateRef.current = "unknown";
      setAgentState("unknown");
      term.options.disableStdin = true;
      setConnection("connecting");
      const current = new WebSocket(wsURL("/ws/term"));
      current.binaryType = "arraybuffer";
      socket = current;
      connectTimer = setTimeout(() => {
        if (current.readyState !== WebSocket.OPEN) current.close();
      }, 12_000);
      current.onopen = () => {
        if (disposed || socket !== current) {
          current.close();
          return;
        }
        clearTimeout(connectTimer);
        retryDelay = 300;
        setConnection("connected");
        sendResize();
      };
      current.onmessage = (message) => {
        if (disposed || socket !== current) return;
        if (typeof message.data !== "string") {
          term.write(
            message.data instanceof ArrayBuffer
              ? new Uint8Array(message.data)
              : message.data,
          );
          return;
        }
        try {
          const control = JSON.parse(message.data);
          if (control?.type === "terminal_reset") {
            term.reset();
            return;
          }
          if (
            control?.type === "agent_state" &&
            ["idle", "running", "stopping"].includes(control.state)
          ) {
            agentStateRef.current = control.state;
            setAgentState(control.state);
            term.options.disableStdin =
              taskActiveRef.current || control.state !== "idle";
            return;
          }
          // Agent events are already rendered in the task pane. PTY bytes are the sole terminal output.
          if (control?.type === "agent_event") return;
        } catch {
          /* Ignore text frames that are not recognized control messages. */
        }
      };
      current.onerror = () => current.close();
      current.onclose = (event) => {
        if (disposed || socket !== current) return;
        clearTimeout(connectTimer);
        setConnection("disconnected");
        agentStateRef.current = "unknown";
        setAgentState("unknown");
        term.options.disableStdin = true;
        if (event.code === SESSION_EXPIRED_CODE) {
          notifySessionExpired();
          return;
        }
        // The shell lives on the server, so reconnecting resumes the same session
        // and replays its output. An expired session is detected by the probe and
        // returns the app to pairing, which unmounts this component.
        void checkSessionAfterDisconnect();
        const delay = retryDelay;
        retryDelay = Math.min(5_000, delay * 2);
        retryTimer = setTimeout(connect, delay);
      };
    };
    reconnectRef.current = () => {
      clearTimeout(retryTimer);
      retryDelay = 300;
      connect();
    };

    const dataSubscription = term.onData((data) => {
      if (taskActiveRef.current || agentStateRef.current !== "idle") return;
      if (socket?.readyState !== WebSocket.OPEN) return;
      // The server closes frames over 64 KiB; split large pastes.
      const bytes = encoder.encode(data);
      for (let i = 0; i < bytes.length; i += 32 * 1024)
        socket.send(bytes.subarray(i, i + 32 * 1024));
    });
    const onResize = () => {
      if (!element.clientWidth || !element.clientHeight || disposed) return;
      fit.fit();
      sendResize();
    };
    const observer = new ResizeObserver(() => {
      cancelAnimationFrame(resizeFrame);
      resizeFrame = requestAnimationFrame(onResize);
    });
    // xterm 5.5 schedules a viewport callback when opened; it can outlive an
    // immediate Strict Mode cleanup. Do not open the discarded probe mount.
    queueMicrotask(() => {
      if (disposed) return;
      term.open(element);
      observer.observe(element);
      connect();
    });

    return () => {
      disposed = true;
      clearTimeout(connectTimer);
      clearTimeout(retryTimer);
      clearTimeout(clipboardTimer);
      cancelAnimationFrame(resizeFrame);
      observer.disconnect();
      dataSubscription.dispose();
      socket?.close();
      term.dispose();
      terminalRef.current = null;
    };
  }, []);

  useEffect(() => {
    if (terminalRef.current)
      terminalRef.current.options.theme = terminalPalette(theme);
  }, [theme]);

  return (
    <>
      <header className="panel-head">
        <div className="panel-title">
          <Icon name="terminal" size={16} />
          <h2>远程终端</h2>
        </div>
        <div className="panel-meta">
          <span className="status-chip">
            <span
              className="dot"
              data-tone={
                connection === "connected"
                  ? readOnly
                    ? "active"
                    : "ok"
                  : connection === "disconnected"
                    ? "bad"
                    : "active"
              }
            />
            <span role="status">
              {clipboardStatus ||
                (connection === "connected"
                  ? agentState === "unknown"
                    ? "同步状态"
                    : agentState === "stopping"
                      ? "停止清理中"
                      : readOnly
                        ? "Agent 控制"
                        : "已连接"
                  : connection === "disconnected"
                    ? "已断开，正在重连"
                    : "连接中")}
            </span>
          </span>
          {connection === "disconnected" && (
            <button
              className="button ghost"
              onClick={() => reconnectRef.current()}
            >
              <Icon name="refresh" size={14} />
              重新连接
            </button>
          )}
        </div>
      </header>
      <div className="term" data-readonly={readOnly} ref={containerRef} />
    </>
  );
}
