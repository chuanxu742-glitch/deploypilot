# 验证

主机：Windows，`go version go1.26.0 windows/amd64`，Node `v22.15.0`。`CGO_ENABLED=0`。

## 结果

| 范围 | 命令 | 结果 |
|---|---|---|
| 后端静态检查 | `go vet ./...` | 退出码 0 |
| 后端全量测试 | `go test ./...` | 退出码 0；8 个有测试的包全部通过（agent、auth、banner、llm、server、tunnel、cmd/deploypilot）。 |
| 前端类型检查 | `npx tsc --noEmit` | 退出码 0 |
| 前端单元测试 | `node --test tests/*.ts`（本机 Node 22.15 需 `--experimental-strip-types`） | 25 项通过，0 失败（含新增 3 项）。 |
| 前端生产构建 | `npm run build`（`tsc -b && vite build`） | 退出码 0，产物写入内嵌目录 `client/internal/webui/dist`。 |

基线：改动前在 ui11 检查点上同样跑过上述命令，均通过（`TestShutdownClosesWebSockets` 当时已偶发失败，见下）。

## 新增/修改的测试

- `client/internal/server/hardening_test.go`
  - `TestTruncateUTF8RuneBoundary` / `TestTruncateUTF8ASCIIUnchanged`：按字节上限截断不切断 rune。
  - `TestBoundedOutputKeepsHeadAndTail` / `...ShortIsVerbatim` / `...RuneSafeBoundaries`：首尾保留、短输出原样、边界 UTF-8 合法。
  - `TestCorruptScrollbackDoesNotDisableSession`：损坏 `terminal.json` 被移到 `.corrupt`，会话照常启动。
  - `TestHistoryStartupErrorNamesDirectory`：启动错误包含历史目录。
  - `TestIdleSessionSendsExpiryCloseCode`：过期的 agent WS 收到关闭码 `4401`。
- `client/internal/auth/hook_test.go`
  - `TestSessionEndedHookFiresOnceOnIdleExpiry`：空闲过期恰好触发一次，live 会话与二次检查不触发。
- `client/internal/server/shutdown_test.go`：去抖（排空连接时快照后再断言关闭）。`-count=15` 稳定通过。
- `web/tests/hardening.test.ts`
  - `isFailedFinal` 仅对显式失败的 final 为真；`SESSION_EXPIRED_CODE === 4401`；会话过期回调注册/清除行为。

## 回归保护

- ui11 既有测试全部仍通过，包括：共享 Shell 的目录/环境变量持久化与人工输入（`TestSharedShellKeepsDirectoryEnvironmentAndManualInput`）、退出码与取消（`TestSharedShellExitStatusAndCancellation`）、任务历史恢复与并发写拒绝、会话过期取消任务、慢订阅者不阻塞 Agent 等。

## 未覆盖 / 待办

- **Linux bash 执行路径**：Windows 主机上 `go test` 只跑共享 Shell 的 PowerShell 分支。本轮对 Linux 分支的改动（pager 环境、就绪等待上限、`set` 复位、输出截断、控制台重配对的 QR 打印）仅经编译、代码审查与字符串/逻辑级单测覆盖，**真机 Linux 端到端验证仍待补**。
- **竞态检测**：`-race` 需要 CGO 与 C 编译器，本机未配置，未运行。
- **浏览器端到端**（`npm run test:browser`，Playwright + Chrome）本轮未运行；前端组件改动通过 `tsc` 与生产构建验证，纯逻辑通过单测验证。
