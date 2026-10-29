# 隧道稳定性验证记录

日期：2026-10-29。对象：当前工作区后端，本地构建版本 `0.2.0-tunnel1`。使用 Windows amd64 / 仓内缓存 Go 1.27.0；保留其他已有工作区改动。测试模型密钥置空，没有调用真实模型。本轮代码、测试、文档和构建完成，不代表生产稳定性获得保证；没有重启现有实例或发布版本。

## 结论与证据

| 结论 | 验证方式 | 结果与局限 |
|---|---|---|
| 原实现存在目标缺陷 | 新增旧接口回归用例，在修改实现前运行 | go test 退出 1；stdout 和大日志启动超时、URL 无连接被误报成功、零状态提前退出报错格式错误、响应头阻塞不切源、残留部分文件、并发下载失败、无 pong 不关闭均复现。`red.log` |
| 已有缺陷修复 | 同一组回归及 tunnel/server 全包 | 退出 0；`green-foundation.log` |
| 恢复与重配对行为受测试保护 | maintain/auth/server/main 回归；通过 Go overlay 临时撤去维护循环撤销通知及 ResetPairing 的旧会话清除，不修改源文件 | 正常包测试退出 0，`green-recovery.log`；撤去保护后两个目标测试均失败，go test 退出 1，`mutation-check.log`。新增维护/token 测试与实现一并加入，未冒称它们先红后绿；overlay 检验补足其区分能力 |
| 全部 Go 回归通过 | `go test ./... -json -count=1 -timeout=90s` | 退出 0，98 个顶层测试通过，5 项跳过（4 项真实模型测试、1 项需显式启用的公网测试）。`all-tests.jsonl`；公网测试另行执行，见下表 |
| 静态检查通过 | `go vet ./...`、本轮范围 `git diff --check` | 退出 0；vet 无输出，`vet.log`。diff 仅提示 Windows 换行转换，无空白错误 |
| 构建通过 | CGO_ENABLED=0，GOOS/GOARCH 分别 windows/amd64、linux/amd64、linux/arm64，`go build -trimpath -ldflags '-X main.version=0.2.0-tunnel1' ... ./cmd/deploypilot` | 三项退出 0，尺寸和 SHA-256 见 `artifacts.json`；Linux 为交叉编译，未做 Linux 运行验证，未做 race 检查 |
| 实际 Windows 程序入口恢复符合上限 | 构建的 deploypilot.exe 使用模拟 cloudflared 子进程，首次连接成功后主动退出，反复触发恢复 | 预期程序退出 1；5 次配对、5 个不同 token、4 次恢复通知、4 次重试、最后报告连续失败上限。检查脚本退出 0；`cli-result.json`。该项验证本地入口和进程恢复，不等同公网链路恢复 |
| 第一次公网短测失败 | 真实 cloudflared 2026.10.5，临时 loopback 健康/echo 服务，Go HTTP/WS 客户端 | 4.706 秒首次边缘连接；9.1 秒 HTTP 可达；WebSocket 握手 EOF，退出 1，`live.log`。没有暴露真实控制台、文件或命令执行接口 |
| 第二次公网短测通过 | 相同公网测试，加 TCP/TLS/HTTP 握手阶段诊断 | 6.308 秒首次边缘连接；7.608 秒 HTTP 可达；TCP、TLS、HTTP 握手成功，至少 30 秒内 7 次 WebSocket echo 往返成功。测试 43.96 秒，退出 0，`live-diagnostic.log` |
| 长期稳定性仍未证明 | 对照观测范围与缺口 | 第一次 EOF 未复现到可定位的具体阶段，原因未确定；第二次通过不撤销失败记录。没有 24/72 小时运行、跨运营商、真实断网恢复或客户端浏览器公网验证，不能推算在线率 |

## 验收条件对应

| 条件 | 直接证据 | 结果 |
|---|---|---|
| T1 两路日志、大日志不堵塞 | TestStartDrainsBothStreamsAndLargeLines | stdout 保持 stderr 打开、2 MiB 无换行输出均可启动 |
| T2 连接确认和取消清理 | TestStartURLAloneIsNotReady、TestStartEarlyCleanExitIsError、真实 Start/Close 子进程测试；公网 pidfile 确认 | URL 单独存在时超时；子进程退出/取消有明确结果，Close 后 Done 已关闭；首次边缘连接不等于全球公网可达 |
| T3 重试、退避、重配对 | TestMaintainRetriesAndRevokesBeforeNewPairing、TestMaintainBoundsFailuresAndBackoff、TestMaintainCancelDuringBackoff、TestMaintainStopsOnPairingFailure、TestMaintainStableRunResetsFailures；auth/recovery_test.go；实际 CLI 检查 | 通过；中断任务不重放；原有 session 守护与服务级任务互斥继续通过全套回归 |
| T4 下载边界 | TestDownloadSkipsStalledHeaders、TestDownloadCanceledCleansPartialFile、TestConcurrentDownloadsUseIndependentFiles，以及原有慢速镜像/校验测试 | 通过；唯一临时文件、取消清理、头部阻塞切源，原有 SHA-256 校验保留 |
| T5 WebSocket 心跳 | TestWebSocketClosesWithoutPong、TestWebSocketPongsKeepConnectionAlive、TestAutomaticPongsDoNotRenewSession | 通过；失联关闭，回复 pong 保活，心跳不延长认证寿命 |
| T6 入口、回归和边界如实报告 | 全套测试、vet、三平台构建、CLI 检查及两次公网短测 | 本地检查通过；公网一次失败一次通过，不能判定长期稳定 |

## 实现与注释核对

按 eri-comment-maintenance 核对本轮改动：移除旧注释中未经本轮证实的“Cloudflare 约 100 秒关闭 WebSocket”固定断言；明确 75 秒由本程序决定，心跳不续认证；说明 pidfile 只代表首次边缘连接、重建后需新配对、不会续接命令；认证包注释更新为每次新隧道生成 token。没有扩展到无关模块重构。

## 原始记录与产物

根目录：`E:/WORK/lulu/deploypilot/.probe-tmp/tunnel-stability-20261029/`（Git 忽略）。上述所有日志名均相对此目录。`before/` 保存本轮修改前的 tunnel、auth.go、keepalive.go、main.go。

- Windows：`deploypilot-windows-amd64.exe`
- Linux：`deploypilot-linux-amd64`、`deploypilot-linux-arm64`
- 构建校验：`artifacts.json`
- CLI 完整输出：`cli-stdout.log`、`cli-stderr.log`，包含测试用随机配对链接，仅留在本机忽略目录，不写入公开产物。
- 公网短测重跑：从 client 目录设置 `DP_TEST_CLOUDFLARED_LIVE_BIN` 为待测二进制路径，再执行 `go test ./internal/tunnel -run '^TestLiveTunnelHealthAndWebSocket$' -v -count=1 -timeout=3m`；会临时公开纯健康/echo 服务，结束自动关闭。

官方边界：[Quick Tunnels](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/) 用于开发测试，不保证在线时间，重建改变域名。代码修复无法消除该服务约束；本轮未配置正式隧道。
