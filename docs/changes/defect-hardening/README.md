# 缺陷修复：前后端加固

在 ui11（持续终端与历史恢复）之上，对前后端扫描出的缺陷做集中修复。分支 `fix/backend-frontend-hardening`，在 ui11 工作树的检查点提交之上逐组提交，每组自带测试。

## 修复清单

### 后端

1. **API Key 环境变量泄漏**：`llm.FromEnv()` 读取后立即 `os.Unsetenv("BAILU_API_KEY")`，子 Shell（PTY）与 AI 命令不再能通过 `env`/`printenv` 读到密钥，也就不会写入日志、审计或发给模型。
2. **模型请求 120 秒整体超时**：改为 60 秒响应头超时 + 无整体超时的传输，长流式回复不再被拦腰切断；同时经 `DefaultTransport` 支持 `HTTP(S)_PROXY`。流仍受 2 MiB 上限与 ctx 约束。
3. **事件按字节截断切断 UTF-8**：`emitTask` 与旧日志导入按 rune 边界截断。
4. **历史损坏可恢复性**：启动因历史损坏而失败时，错误信息带上目录与恢复提示（保留 ui11 的 fail-closed 数据安全策略）；损坏的 `terminal.json`（易失回滚缓冲，非任务历史）移到 `terminal.json.corrupt` 并以空终端启动，不再使终端与 Agent 永久不可用。
5. **任务历史响应在锁内写网络（N4）**：`handleTasks` 在 `taskMu` 内完成序列化，锁外再写连接，慢客户端不再阻塞 Agent 输出与终端订阅者。
6. **Linux 共享 Shell 加固**：
   - 导出 `PAGER=cat`/`GIT_PAGER=cat`/`SYSTEMD_PAGER=`/`DEBIAN_FRONTEND=noninteractive`，并 `export PROMPT_COMMAND`。
   - 就绪等待设 30 秒上限：遇到交互式前台程序（分页器/编辑器/sudo/su/未完成输入）快速失败并给出清晰原因，不再等满命令超时。
   - 每条命令前 `set +e +u +o pipefail`，上一条命令泄漏的选项不会误杀下一条。
   - 命令输出改为 rune 安全的 16 KiB 首 + 16 KiB 尾，保留结尾处的报错。
7. **会话过期信令 + 重新配对**：
   - 过期/吊销的 WebSocket 关闭前发送应用关闭码 `4401`。
   - `auth` 新增一次性 `SetSessionEndedHook`，空闲过期时触发。
   - 空闲过期且隧道在线时，控制台自动重新打印二维码/链接，无需重启程序即可重新接入。

### 前端

8. **任务启动被拒不可见**：被拒（无记录的 error 事件）时在输入区显示原因并恢复草稿，不再清空草稿、把空日志显示成“已结束”。
9. **失败任务与成功任务外观相同**：`final.success === false` 以失败样式（告警图标、红色、标题“任务未完成”）渲染。
10. **会话过期前端无感知**：识别 `4401` 关闭码，Agent 停止无限重连，应用回到配对页，提示重新扫码。

### 文档与测试

11. README 更正：`DP_INSTALL_DIR` 默认值、发布流程（实为 `publish.ps1`，无 GitHub Actions）、重新配对、代理、API Key 环境清理等。
12. 顺带修复 ui11 原有的 flaky 测试 `TestShutdownClosesWebSockets`（单次读可能先读到连接时快照）。

## 刻意未做（附理由）

- **Linux 命令中 `exit` / 命令内 `set -e` 的完全隔离**：需要子进程/子 Shell，和 ui11 的“环境变量跨命令持久化”特性（有测试约束）冲突。仅做了跨命令的选项复位；`exit` 导致 Shell 退出时依赖既有的 Shell 自动重建兜底。
- **`writeTerminalInput` 持 `agentMu` 做阻塞写（Issue 11）**：改成锁外写会重新引入 ui11 刻意规避的竞态（任务取得所有权后仍写入人工输入）。属低严重度，窄触发条件，保持原状。
- **N5（Linux 终端回显真实命令）**：需重排标记与 `dp` 函数，属外观问题，且无法在 Windows CI 上实机验证。
- **功能缺失类**（文件上传下载、Markdown 渲染、国际化、多终端、注销接口、任务删除、token 用量统计等）：属新功能，不在本轮“修复缺陷”范围。

## 已知限制

- 开发/CI 主机为 Windows，`go test` 只执行共享 Shell 的 PowerShell 分支。Linux bash 分支仅编译与字符串级单测覆盖（`boundedOutput`、就绪等待逻辑），**真机 Linux 验证仍待补**。这与 ui11 文档的既有说明一致。
