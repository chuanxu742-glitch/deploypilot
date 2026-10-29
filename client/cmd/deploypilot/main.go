// Command deploypilot is the remote deploy assistant client.
//
// It renders a welcome banner, starts a local web UI bound to loopback,
// exposes it through a Cloudflare quick tunnel, and shows a QR code that
// operators scan to pair and control the machine.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

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

func run() error {
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

	srv := server.New(server.Options{Version: version, Assets: assets, Auth: authMgr, LLM: llmClient})
	addr, err := srv.Start()
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}

	port, err := portOf(addr)
	if err != nil {
		return err
	}

	fmt.Println("正在准备安全隧道，请稍候…")
	binPath, err := tunnel.EnsureBinary(ctx)
	if err != nil {
		return fmt.Errorf("prepare cloudflared: %w", err)
	}

	tun, err := tunnel.Start(ctx, binPath, port)
	if err != nil {
		return fmt.Errorf("start tunnel: %w", err)
	}
	defer tun.Close()

	link := fmt.Sprintf("%s/#t=%s", tun.URL, authMgr.Token())
	fmt.Println()
	fmt.Println("用手机或电脑扫描下方二维码接入：")
	fmt.Println()
	tunnel.PrintQR(os.Stdout, link)
	fmt.Println()
	fmt.Println("或直接打开链接：")
	fmt.Println("  " + link)
	fmt.Println()
	fmt.Println("按 Ctrl+C 结束会话并关闭隧道。")

	select {
	case <-ctx.Done():
		fmt.Println("\n正在关闭…")
	case <-tun.Done():
		fmt.Fprintln(os.Stderr, "\n隧道意外退出：", tun.Err())
	}

	authMgr.Revoke()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func portOf(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("parse addr %q: %w", addr, err)
	}
	return strconv.Atoi(portStr)
}
