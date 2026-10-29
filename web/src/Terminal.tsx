import { useEffect, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

/** Builds an absolute ws(s):// URL for a same-origin path. */
function wsURL(path: string): string {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}${path}`;
}

export function Terminal() {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const term = new XTerm({
      fontFamily: '"Cascadia Code", "JetBrains Mono", Menlo, Consolas, monospace',
      fontSize: 13,
      cursorBlink: true,
      theme: {
        background: "#0e1420",
        foreground: "#e6edf3",
        cursor: "#35d0ba",
      },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(containerRef.current);
    fit.fit();

    const encoder = new TextEncoder();
    const decoder = new TextDecoder();
    let socket: WebSocket | null = null;
    let disposed = false;
    let waitingForKey = false;

    const sendResize = () => {
      if (socket && socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
    };

    const connect = () => {
      waitingForKey = false;
      const s = new WebSocket(wsURL("/ws/term"));
      s.binaryType = "arraybuffer";
      socket = s;

      s.onopen = () => {
        term.writeln("\x1b[36m已连接到远程终端。\x1b[0m");
        sendResize();
        term.focus();
      };
      s.onmessage = (ev) => {
        if (typeof ev.data === "string") {
          term.write(ev.data);
        } else {
          term.write(decoder.decode(new Uint8Array(ev.data), { stream: true }));
        }
      };
      s.onclose = (ev) => {
        if (disposed || socket !== s) return;
        const why = ev.reason ? `（${ev.reason}）` : "";
        term.writeln(`\r\n\x1b[33m连接已断开${why}。按任意键重新连接。\x1b[0m`);
        waitingForKey = true;
      };
    };

    const dataSub = term.onData((data) => {
      if (waitingForKey) {
        term.writeln("\x1b[36m正在重新连接…\x1b[0m");
        connect();
        return;
      }
      if (socket && socket.readyState === WebSocket.OPEN) {
        socket.send(encoder.encode(data));
      }
    });

    const onResize = () => {
      fit.fit();
      sendResize();
    };
    window.addEventListener("resize", onResize);

    connect();

    return () => {
      disposed = true;
      window.removeEventListener("resize", onResize);
      dataSub.dispose();
      socket?.close();
      term.dispose();
    };
  }, []);

  return <div className="term" ref={containerRef} />;
}
