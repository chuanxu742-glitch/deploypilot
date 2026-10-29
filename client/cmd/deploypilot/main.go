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
	"syscall"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/deploypilot/deploypilot/internal/banner"
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
	banner.Render(os.Stdout, banner.DefaultOptions(os.Stdout, version))

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
	srv := server.New(server.Options{Version: version, Assets: assets, Auth: authMgr, LLM: llmClient, AgentContextBytes: contextBytes, HistoryDir: historyDir, LegacyAuditDir: auditDir})
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

	connected := false
	err = tunnel.Maintain(ctx, binPath, port, tunnel.Callbacks{
		Unavailable: authMgr.RevokeAll,
		Retry: func(cause error, delay time.Duration) {
			fmt.Fprintf(os.Stderr, "\n隧道暂不可用：%v；%s 后重试。\n", cause, delay)
		},
		Ready: func(publicURL string) error {
			token, err := authMgr.ResetPairing()
			if err != nil {
				return err
			}
			if connected {
				fmt.Println("\n隧道已重新建立。旧会话已失效，请使用新的二维码或链接重新配对；中断的任务不会自动重跑。")
			}
			connected = true
			link := fmt.Sprintf("%s/#t=%s", publicURL, token)
			fmt.Println("\n用手机或电脑扫描下方二维码接入：")
			tunnel.PrintQR(os.Stdout, link)
			fmt.Println("\n或直接打开链接：")
			fmt.Println("  " + link)
			fmt.Println("\n按 Ctrl+C 结束会话并关闭隧道。")
			return nil
		},
	})
	if ctx.Err() != nil {
		fmt.Println("\n正在关闭…")
	}
	return err
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

func portOf(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("parse addr %q: %w", addr, err)
	}
	return strconv.Atoi(portStr)
}
