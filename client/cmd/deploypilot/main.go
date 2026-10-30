// Command deploypilot is the remote deploy assistant client.
//
// It renders a welcome banner, starts a local web UI bound to loopback,
// exposes it through a Cloudflare quick tunnel, and shows a QR code that
// operators scan to pair and control the machine.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/deploypilot/deploypilot/internal/banner"
	"github.com/deploypilot/deploypilot/internal/console"
	"github.com/deploypilot/deploypilot/internal/llm"
	"github.com/deploypilot/deploypilot/internal/server"
	"github.com/deploypilot/deploypilot/internal/tunnel"
	"github.com/deploypilot/deploypilot/internal/webui"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "deploypilot:", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	contextBytes, err := contextBudget(os.Getenv("DP_CONTEXT_BYTES"))
	if err != nil {
		return err
	}
	maxSteps, err := stepBudget(os.Getenv("DP_MAX_STEPS"))
	if err != nil {
		return err
	}
	caps, restoreConsole := console.Prepare(os.Stdout)
	defer restoreConsole()
	banner.Render(os.Stdout, banner.DefaultOptions(caps, version))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	assets, err := webui.FS()
	if err != nil {
		return fmt.Errorf("load web assets: %w", err)
	}

	authMgr, err := auth.NewManager(auth.Options{})
	if err != nil {
		return fmt.Errorf("init auth: %w", err)
	}

	llmClient := llm.FromEnv()
	// The key now lives in the in-memory client only. Drop it from the process
	// environment so child shells (the PTY) and agent commands cannot read it
	// back via `env`/`printenv`, which would also leak it to logs and the model.
	_ = os.Unsetenv("BAILU_API_KEY")

	auditDir, err := agent.DefaultAuditDir()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	workspaceID := fmt.Sprintf("%x", sha256.Sum256([]byte(cwd)))[:16]
	historyDir := filepath.Join(auditDir, "history", workspaceID)
	srv := server.New(server.Options{Version: version, Assets: assets, Auth: authMgr, LLM: llmClient, AgentContextBytes: contextBytes, AgentMaxSteps: maxSteps, HistoryDir: historyDir, LegacyAuditDir: auditDir})
	addr, err := srv.Start()
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	// Also clean up if binary preparation or tunnel startup fails.
	defer func() {
		authMgr.RevokeAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, srv.Shutdown(shutdownCtx))
	}()

	port, err := portOf(addr)
	if err != nil {
		return err
	}

	fmt.Println("正在准备安全隧道，请稍候…")
	binPath, err := tunnel.EnsureBinary(ctx)
	if err != nil {
		return fmt.Errorf("prepare cloudflared: %w", err)
	}

	// pairingURL holds the current public tunnel URL, or nil while the tunnel is
	// down. The session-ended hook uses it to reprint a link only when a tunnel
	// is actually up.
	var pairingURL atomic.Pointer[string]
	printLink := func(header, publicURL, token string) {
		link := fmt.Sprintf("%s/#t=%s", publicURL, token)
		fmt.Println(header)
		fmt.Print(qrBlock(caps, link))
		fmt.Println("\n或直接打开链接：")
		fmt.Println("  " + link)
	}
	// When the single session idle-expires, mint a fresh token and reprint the
	// QR to the console so the operator (who can see this terminal) can pair
	// again. Without this the only way back was to restart the program.
	authMgr.SetSessionEndedHook(func() {
		u := pairingURL.Load()
		if u == nil {
			return
		}
		token, err := authMgr.ResetPairing()
		if err != nil {
			return
		}
		printLink("\n会话已空闲超时并结束。如需重新接入，请扫描新的二维码或打开下方链接：", *u, token)
	})

	connected := false
	err = tunnel.Maintain(ctx, binPath, port, tunnel.Callbacks{
		Unavailable: func() { pairingURL.Store(nil); authMgr.RevokeAll() },
		Retry: func(cause error, delay time.Duration) {
			fmt.Fprintf(os.Stderr, "\n隧道暂不可用：%v；%s 后重试。\n", cause, delay)
		},
		Ready: func(publicURL string) error {
			token, err := authMgr.ResetPairing()
			if err != nil {
				return err
			}
			u := publicURL
			pairingURL.Store(&u)
			if connected {
				fmt.Println("\n隧道已重新建立。旧会话已失效，请使用新的二维码或链接重新配对；中断的任务不会自动重跑。")
			}
			connected = true
			printLink("\n用手机或电脑扫描下方二维码接入：", publicURL, token)
			fmt.Println("\n按 Ctrl+C 结束会话并关闭隧道。")
			return nil
		},
	})
	if ctx.Err() != nil {
		fmt.Println("\n正在关闭…")
	}
	return err
}

// qrBlock returns the pairing QR code drawn the way this terminal can show it,
// or a note pointing at the link when it would be garbled or wrap.
func qrBlock(caps console.Caps, link string) string {
	mode := caps.QR()
	if mode == console.QRNone {
		return "（当前终端无法正确显示二维码。可改用 Windows Terminal 运行，或使用下方链接。）\n"
	}
	text, columns := tunnel.QR(link, mode == console.QRHalfBlocks)
	if caps.Width > 0 && columns > caps.Width {
		return fmt.Sprintf("（窗口宽度 %d 列，二维码需要 %d 列，显示会错乱。请把窗口拉宽后重启，或使用下方链接。）\n", caps.Width, columns)
	}
	return text
}

func contextBudget(value string) (int, error) {
	if value == "" {
		return agent.DefaultContextBytes, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 16*1024 || n > 2*1024*1024 {
		return 0, fmt.Errorf("DP_CONTEXT_BYTES must be an integer from 16384 to 2097152")
	}
	return n, nil
}

func stepBudget(value string) (int, error) {
	if value == "" {
		return agent.DefaultMaxSteps, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 5 || n > 200 {
		return 0, fmt.Errorf("DP_MAX_STEPS must be an integer from 5 to 200")
	}
	return n, nil
}

func portOf(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("parse addr %q: %w", addr, err)
	}
	return strconv.Atoi(portStr)
}
