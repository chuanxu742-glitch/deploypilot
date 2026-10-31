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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The intro plays while the server starts. Ctrl+C or an early error jumps
	// to the finished screen so nothing prints over a half-drawn banner.
	bannerOpts := banner.DefaultOptions(caps, version)
	intro := banner.Play(ctx, os.Stdout, bannerOpts)
	defer intro.Stop()

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

	progress := intro.StartProgress("正在准备连接组件")
	defer progress.Finish("")
	binPath, err := tunnel.EnsureBinary(ctx)
	if err != nil {
		if ctx.Err() != nil {
			progress.FinishCancelled("连接准备已取消")
		} else {
			progress.FinishFailure("连接组件准备失败")
		}
		return fmt.Errorf("prepare cloudflared: %w", err)
	}
	progress.Set("正在建立安全隧道")

	// pairingURL holds the current public tunnel URL, or nil while the tunnel is
	// down. The session-ended hook uses it to reprint a link only when a tunnel
	// is actually up.
	var pairingURL atomic.Pointer[string]
	printLink := func(header, publicURL, token string) {
		link := fmt.Sprintf("%s/#t=%s", publicURL, token)
		fmt.Print(banner.RenderPairing(bannerOpts, pairingFor(caps, header, link, authMgr.TokenTTL())))
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
		fmt.Print("\n" + banner.Status(bannerOpts, banner.ToneInfo, "会话空闲超时，已结束", "已生成新的配对二维码"))
		printLink("扫码重新接入", *u, token)
	})

	connected := false
	err = tunnel.Maintain(ctx, binPath, port, tunnel.Callbacks{
		Unavailable: func() { pairingURL.Store(nil); authMgr.RevokeAll() },
		Retry: func(cause error, delay time.Duration) {
			if !connected {
				progress.Set(fmt.Sprintf("连接暂不可用：%v · %s 后重试", cause, delay.Round(time.Second)))
				return
			}
			fmt.Print("\n" + banner.Status(bannerOpts, banner.ToneWarning,
				fmt.Sprintf("隧道暂不可用，%s 后重试", delay.Round(time.Second)), cause.Error()))
		},
		Ready: func(publicURL string) error {
			token, err := authMgr.ResetPairing()
			if err != nil {
				progress.FinishFailure("配对准备失败")
				return err
			}
			u := publicURL
			pairingURL.Store(&u)
			if connected {
				fmt.Print("\n" + banner.Status(bannerOpts, banner.ToneSuccess, "隧道已重新建立",
					"网址已更换，旧会话已失效；中断的任务不会自动重跑"))
				printLink("扫码重新配对", publicURL, token)
			} else {
				progress.FinishSuccess("安全隧道已就绪")
				printLink("用手机扫码，打开工作台", publicURL, token)
			}
			connected = true
			return nil
		},
	})
	if ctx.Err() != nil {
		progress.FinishCancelled("连接已停止")
	} else if err != nil {
		progress.FinishFailure("连接失败")
	}
	if ctx.Err() != nil {
		fmt.Print("\n" + banner.Status(bannerOpts, banner.ToneInfo, "正在关闭…", ""))
	}
	return err
}

// pairingFor describes the pairing block for link: the QR code drawn the way
// this terminal can show it, or a notice pointing at the link when it would
// be garbled or wrap.
func pairingFor(caps console.Caps, heading, link string, ttl time.Duration) banner.Pairing {
	p := banner.Pairing{
		Heading: heading,
		Link:    link,
		Notes: []string{
			fmt.Sprintf("%d 分钟内有效，仅限配对一次", max(1, int(ttl.Round(time.Minute)/time.Minute))),
			"同一时间只能一台设备接入",
			"",
			"保持此窗口开启",
			"按 Ctrl+C 结束连接",
		},
	}
	mode := caps.QR()
	if mode == console.QRNone {
		p.Notice = "当前终端无法正确显示二维码，可改用 Windows Terminal 运行，或使用下方链接。"
		return p
	}
	text, columns := tunnel.QR(link, mode == console.QRHalfBlocks)
	if caps.Width > 0 && columns > caps.Width {
		p.Notice = fmt.Sprintf("窗口宽度 %d 列，二维码需要 %d 列。请把窗口拉宽后重启，或使用下方链接。", caps.Width, columns)
		return p
	}
	p.QR, p.QRColumns, p.QRHalfBlocks = text, columns, mode == console.QRHalfBlocks
	return p
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

// stepBudget reads DP_MAX_STEPS. Unset or 0 means no cap (the default, as in
// pi and oh-my-pi); a cap, when wanted, must allow at least 5 turns.
func stepBudget(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || (n > 0 && n < 5) {
		return 0, fmt.Errorf("DP_MAX_STEPS must be 0 (no limit) or an integer of at least 5")
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
