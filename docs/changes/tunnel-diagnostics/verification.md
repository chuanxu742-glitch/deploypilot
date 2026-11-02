状态与批准记录见本目录 README。

# 验证与排查结果

## 当前判断

1. DeployPilot 原有诊断存在两个已证实缺陷：提取临时网址后丢弃底层错误，启动进度行截断长错误和日志路径。本次已修复，并在本机安装的诊断构建实际运行到首次超时时核对输出。
2. 握手失败不依赖 DeployPilot 启动包装：同一网络下，独立运行三个官方 cloudflared 版本都能申请临时网址，但默认协议和强制 HTTP/2 均未建立首次连接。本次没有发现切换到这两个替代版本能恢复连接的证据，默认固定版本保持 `2026.10.5`。
3. 故障范围已缩小到当前代理路径上的 Cloudflare 隧道握手。不能进一步断定是代理服务器策略、其上游网络还是目标端策略；没有切换全局代理节点或做另一网络对照。

## 版本对照

同一 Windows 主机，保持现有全局代理模式、TUN 和所选节点；每次独立空配置，仅提供 cloudflared 内置测试页，无真实控制台。默认协议为 QUIC；HTTP/2 为显式参数。每组观察 65 秒，以下每格是 1 次启动的结果，不代表长期可用率。

| 官方版本 | 默认协议 | 强制 HTTP/2 | 临时网址 |
|---|---|---|---|
| 2026.9.3 | QUIC 握手持续超时；未就绪 | `TLS handshake with edge error: EOF`；未就绪 | 两次均取得 |
| 2026.10.5 | QUIC 握手持续超时；未就绪 | `TLS handshake with edge error: EOF`；未就绪 | 两次均取得 |
| 2026.10.7 | QUIC 握手持续超时；未就绪 | `TLS handshake with edge error: EOF`；未就绪 | 两次均取得 |

官方发布元数据来自 GitHub Releases API，二进制逐一核对其中的 SHA-256：

| 版本 | SHA-256 |
|---|---|
| 2026.9.3 | `f096265ec2fcbe9bb6e2d64268db167ced3fcbb83d894bdb9e2fcdb26f2ea7e2` |
| 2026.10.5 | `db4e4c413612a0916f3e1679d3caaf376f94911952d163028fb726fb2e0516a9` |
| 2026.10.7 | `2493dbc168923bb3e59e5e7716f71deced92992eea9acd6e9d74f84ac7751343` |

原始结果：`.probe-tmp/tunnel-diagnostics/results.json`；每组日志在同目录的 `<版本>-<default|http2>/cloudflared.log`。六组测试进程均回收。最后一组结束时代理状态读取失败，随后补读成功且模式/节点与各组启动时一致，见 `proxy-after-recheck.json`；这些只是离散观察，不证明两次观察之间完全没有外部变化。

附加对照：使用独立 Python TLS 客户端，对两个入口分别经 TUN、显式本机 SOCKS 连接，四次均在 TLS 阶段遇到 EOF。SOCKS 协商成功，因此这不是只有 TUN 接入才出现的症状。使用 `h2.cftunnel.com` 作为服务器名称，未关闭证书校验。记录 `transport-probes.json`；端口和服务器名称依据 [Cloudflare 官方配置说明](https://developers.cloudflare.com/tunnel/configuration/)。

尝试订阅代理错误流未取得响应头和可用记录，不能解读为代理没有报错；记录 `proxy-errors-state.json`。未修改代理配置或进行代理节点切换。

## 代码与实际入口验证

| 结论 | 验证方式 | 结果与局限 |
|---|---|---|
| 改前隧道包基线通过 | `go test ./internal/tunnel -count=1 -timeout=60s` | 正常本机环境退出 0，4.560s。本聊天命令输出为原记录。首次沙箱检查因回环网络/进程权限限制失败，不能算通过。 |
| 回归用例能检出原缺陷 | `go test ./internal/tunnel -run '^TestStartReportsUnderlyingErrorAfterURL$' -count=1 -v -timeout=30s` | 改前退出 1：实际只有 `cloudflared exited: exit status 7`，缺失预期 TLS 错误；改后退出 0。原始红绿结果在本聊天对应命令输出。 |
| 日志及启动边界通过 | `go test ./internal/tunnel -run 'Test(Start\|Diagnostic)' -count=1 -v -timeout=60s` | 退出 0，6.353s；覆盖两种超时阶段、URL 后错误、配置/凭证过滤、跨写入与末行、内存/文件上限、日志不可写、进程日志隔离、已有输出排空与就绪条件。测试使用本机子进程替身，不代表公网连通。 |
| Go 回归通过 | `go test ./... -count=1 -timeout=120s` | 退出 0，全部 8 个有测试包通过，PTY/webui 无测试；未启用收费模型或可选真实模型测试。记录 `go-tests.log`。 |
| 最后入口显示改动通过 | `go test ./cmd/deploypilot ./internal/banner -count=1 -timeout=60s` | 全套后补的重试完整输出改动，相关包再次通过，退出 0；记录 `go-entry-tests.log`。 |
| 静态检查通过 | `go vet ./...`，最后入口改动后 `go vet ./cmd/deploypilot` | 均退出 0；全套记录 `go-vet.log`。 |
| 本机构建通过 | `go build -ldflags '-s -w -X main.version=v0.4.9-diagnostics' -o '../.probe-tmp/tunnel-diagnostics/deploypilot.exe' ./cmd/deploypilot` | 退出 0；Windows amd64 诊断版，未公开发布。 |
| 本机安装与构建一致 | 先校验旧文件备份，再原子替换 `%LOCALAPPDATA%\DeployPilot\deploypilot.exe` 并核对 SHA-256 | 相同；记录 `installation.json`。旧文件保存在 `.probe-tmp/tunnel-diagnostics/installed-v0.4.9-backup.exe`。 |
| 实际安装入口能提供诊断信息 | 运行安装位置程序 121.31 秒，到首次真实网络超时后终止测试 | 输出 `first edge connection; tunnel URL received (2m0s)`、底层 QUIC 超时和完整可读日志路径；`installed-verification.json` 的阶段/底层错误/日志可读/进程关闭检查均为 true。输出 `installed-run.log`，本机实际日志位置由该记录给出。测试使用重定向的 CLI 输出，未做交互终端截图验收。 |

以上 `.log`、`.json`、脚本和二进制均位于本机 Git 忽略的 `.probe-tmp/tunnel-diagnostics/`；原始记录含本机环境，不上传仓库。已按 `eri-comment-maintenance` 核对本次注释，说明日志的资源生命周期、容量限制、写失败不影响隧道，以及完整错误输出不能走截断进度行的原因。

## 交付边界

- 已修复诊断日志与错误显示，不宣称隧道连通已恢复。
- 未升级默认 cloudflared，未改变 120 秒等待、5 次失败策略或连接成功判据。
- 本机安装已更新；在线安装脚本仍获取线上旧版，重新执行该脚本会覆盖本地诊断版。本次没有推送或公开发布。
- 下一项能区分剩余原因的检查是换一个代理出口或网络做相同握手对照；当前节点和本机全局配置保持原状。
