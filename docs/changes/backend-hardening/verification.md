# 后端可靠性与上下文管理验证

状态与授权见本目录 README.md。2026-10-29，亲自执行；对象为当前未提交工作区，基于 `132c524`，含此前界面修改。实现、测试与构建已完成；未提交、推送、发布或替换现有运行会话。

## 实际交付

- 修复取消后继续执行文件工具、Windows 遗留子进程、WebSocket 会话过期无效、跨连接绕过清理等待、截断响应误报成功、浏览器 Cookie 提前到期六项问题。
- 现有六类工具增加参数验证和可见错误；finish 必须明确提供 summary 和 success。取消在工具执行前、事件发送后、工具返回后均检查。文件调用已经开始后的系统 I/O 仍不具备事务回滚能力。
- 本地上下文压缩不新增模型请求。请求 JSON 默认上限 128 KiB（含工具定义）；`DP_CONTEXT_BYTES` 可设为 16384–2097152。先缩短较早工具结果，再把完整调用轮次整理为命令、路径、参数大小与结果摘要；历史摘要自身有容量上限。保留系统约束和初始任务；不足以容纳原始目标时报错。
- Windows 作业在用户命令开始执行前绑定进程，取消/超时结束关联子进程，正常启动的后台服务保留。实现依据为 Microsoft 的 [AssignProcessToJobObject](https://learn.microsoft.com/en-us/windows/win32/api/jobapi2/nf-jobapi2-assignprocesstojobobject) 和 [Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)；实际行为另由下面的真实进程测试验证。没有把这一机制称作沙箱。

## 证据表

所有原始记录位于仓库本机忽略目录 `.probe-tmp/backend-hardening-20261029/`。Go 使用既有缓存 `go1.27.0.windows-amd64` 工具链，验证进程将 BAILU_API_KEY 清空；没有读取真实密钥或发送真实模型任务。

| 结论 | 验证方式 | 结果与局限 |
|---|---|---|
| 后端回归通过 | `cd client; go test -count=1 -json ./...`；追加两条会话边界测试后重跑 `go test -count=1 -json ./internal/server` | 两次退出码均 0。全套首次 77 个顶层测试通过，追加两项后 server 24 项通过；按包名和测试名去重，最终覆盖 79 个顶层测试通过、4 个真实模型测试跳过、0 失败。计入嵌套测试事件为 111 pass，不能把它误作 111 个独立顶层测试。原始记录 `go-final.jsonl`、`session-final.jsonl`；汇总 `test-summary.json`。 |
| Go 静态检查通过 | `go vet ./...` | 退出码 0，`vet-final.txt`。未使用 race detector。 |
| 前端状态及样式回归通过 | `npm --prefix web test` | 退出码 0，13 项通过、0 失败，`frontend-tests.txt`。本次没有修改前端源码，未重跑视觉验收或真实浏览器联调。 |
| 三个平台构建通过 | `GOOS/GOARCH` 分别设为 windows/amd64、linux/amd64、linux/arm64，`CGO_ENABLED=0 go build -ldflags '-X main.version=0.2.0-backend1' -o <产物> ./cmd/deploypilot` | 三项退出码均 0，`builds.txt`。Linux 只有交叉编译证据，没有运行证据。产物校验值见 `artifacts.json`。 |
| 新二进制读取配置入口 | 将 `DP_CONTEXT_BYTES=invalid` 后运行新 Windows 二进制 | 程序退出码 1（预期），明确报允许范围，在启动服务或隧道前结束；验证脚本退出码 0，`binary-config-check.txt`。该检查只证明配置拒绝入口，不代表真实隧道已联调。 |
| 修改没有空白错误 | `git diff --check -- client README.md docs/changes/backend-hardening` | 最终退出码 0；实际输出在本会话命令记录。最初发现 README 尾部多空行，清理后通过；Git 的 LF/CRLF 提示不是测试失败。 |

## 验收条件对应

| 验收条件 | 由什么验的 | 结果 |
|---|---|---|
| B1：取消后不再开始剩余工具，文件错误可见 | agent 的 `TestCancelledBatchDoesNotWrite`、`TestCancellationWhilePublishingToolCallPreventsFileOperation`、`TestFileFailureIsEmitted` | 通过；覆盖同批次和发送工具事件期间取消，4 类文件操作均未开始。已开始的文件系统调用不能撤销。 |
| B2：Windows 取消/超时清理进程树，正常后台服务保留 | `TestWindowsChildStopsOnCancellation`、`TestWindowsJobPreservesSuccessAndStopsTimeout` | 通过；亲自启动隐藏 PowerShell 子进程，验证取消/超时后消失，正常完成后仍存在，测试最终清理自身子进程。见 `cancellation-green.txt`、`background-green.txt` 和最终全套记录。 |
| B3：HTTP/WS 同一到期规则，真实活动续期，心跳不续期，过期取消任务 | auth 已有空闲续期测试；server 的 `TestIdleSessionClosesSocket`、`TestRevokedSessionClosesSocket`、`TestSocketActivityRenewsSession`、`TestAutomaticPongsDoNotRenewSession`、`TestSessionExpiryCancelsActiveTask` | 通过；缩短超时测试，不等待真实 30 分钟。agent 连接实测；terminal 使用同一守护及消息验证，终端在 Linux 的实际 shell 未运行。 |
| B4：全服务任务互斥保持到清理结束 | `TestReconnectWaitsForOldCleanup`；既有停止后重启和断线取消测试 | 通过；清理期间新连接收到 error 和 idle，释放后可以启动。 |
| B5：不完整流不能执行工具或成功结束，部分流不重放 | `TestIncompleteStreamCannotSucceed`、`TestRejectIncompleteAndMalformedStreams`、`TestStreamTotalSizeIsBounded`、既有 500 重试测试 | 通过；覆盖 EOF、length、过滤、坏 JSON、流内错误、缺失/重复 ID、无效参数 JSON和响应容量；已开始的流出错只请求一次，HTTP 500 在响应流前仍可重试。 |
| B6：Cookie 不提前终止活跃服务端会话 | `TestCookieLifetimeDelegatesToServer`、auth 空闲期限测试、WebSocket 续期测试 | 通过；Cookie 是浏览器会话 Cookie，服务端负责空闲期限。未做浏览器持续 30 分钟测试。 |
| B7：工具参数验证，结果 ID 对应，finish 完整 | `TestInvalidToolArgumentsHaveNoSideEffects`、`TestEmptyFileContentIsValid`、`TestFileFailureIsEmitted` | 通过；缺失、空值、null、错误类型、额外字段、负数/过大超时、空替换片段均拒绝；合法空文件内容仍可写；错误带原调用 ID 并显示。 |
| B8：压缩有界，保留原目标、约束、完整调用关系，并通知操作员 | `TestContextCompactionBoundsRequestsAndKeepsPairs`、`TestOversizedTaskFailsBeforeModelRequest`、`TestContextLargeArgumentsKeepWholeToolRounds`、`TestContextClippingPreservesUnicodeAndTail`、`TestContextBudgetConfiguration` | 通过；模拟 15 次长输出工具调用和最终完成共 16 次模型请求，每次在指定 24 KiB 内；压缩事件出现，原目标、工具配对、UTF-8、输出尾部、文件路径和失败结果保留；超大目标未发送模型请求。 |

## 改前改后对照

- `reliability-red.txt`：改前取消后仍写入、Windows 子进程仍活着、过期 socket 不关闭、活动未续期、重连提前启动、Cookie 固定 MaxAge；改后见分片 green 和最终回归。
- `protocol-red.txt`：8 种坏流与超大响应原先被接受；改后拒绝，且不重试部分流。
- `tools-red.txt`：缺少 content 会清空文件、空 old_string 会插入多处、无 summary/success 的 finish 直接结束；改后都被拒绝。
- `context-red.txt`：原先第 2–8 次请求增长到约 84–554 KiB，超大任务仍请求模型；改后增强为 16 次请求，全部满足 24 KiB 预算且保留原目标和调用配对。
- `cancel-event-red.txt`：发送 tool_call 时取消仍会开始读写文件；增加发送后检查后，同一组用例通过，见 `cancel-event-green.txt`。
- Windows 后台服务新增测试初次因 `exec: WaitDelay expired before I/O complete` 失败，实际子进程仍活着；依据本机 Go `os/exec` 源码，修正为父命令成功退出、后台输出脱离。失败记录在 `boundaries-green.txt`（该文件名来自最初预期，内容实际包含 FAIL），通过记录 `background-green.txt`。不把失败记录称为通过。

## 注释与范围核查

按 eri-comment-maintenance 检查本轮涉及说明。补充服务级占用、WebSocket 自动心跳不续期、工具事件可能阻塞需再次检查取消、Windows 先绑定作业再执行/正常保留后台服务，以及上下文字节预算和完整轮次边界；将旧的“执行器仅用 bash”改为平台 shell。构建标签的变化属于 Windows 实现接入，已由三平台编译验证。

当前前端还有其他既有/同时发生的工作区修改，本次不回滚、不改写它们。未创建提交，因此没有新建面向提交的 issue，也没有更改远端正文。

## 证据缺口

- 真实模型与 Cloudflare：没有调用，4 个 live 测试跳过；供应商对严格结束原因/工具身份协议的兼容性仍需真实联调。已使用现有标准模拟 SSE 测试保持正常调用兼容。
- Linux：amd64、arm64 构建成功，运行时未验。本机 Docker Linux 引擎未启动，查询返回 pipe 不存在；没有为测试启动或改动 Docker 环境。
- 数据竞争检测：未运行，当前 CGO_ENABLED=0，PATH 中未发现 gcc/clang；普通并发测试不等于 race detector。
- 压缩：本地结构化摘录，不是语义模型摘要；会遗失较早输出细节。预算是 JSON 字节，不是供应商 token 数；没有自动识别模型窗口或增加输出 token 配额。
- 本次交付为源码与新构建。现有运行实例未重启，不能把本次测试说成现有远程会话已自动更新。

## 构建产物

本机目录：`.probe-tmp/backend-hardening-20261029/`。

- `deploypilot-windows-amd64.exe`
- `deploypilot-linux-amd64`
- `deploypilot-linux-arm64`

版本均为 `0.2.0-backend1`；校验值和大小保存在同目录 `artifacts.json`。没有公开发布这些文件。
