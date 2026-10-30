状态与批准范围见本目录 README。

## 本轮结果

六项首版已实现：命令完整日志落盘，write/edit 覆盖前备份，连续重复三次提醒、第四次暂停，URL/日志/PID 等待，模型流 180 秒空闲超时与有限分钟级退避，成功 finish 关联实际成功的验证调用。默认上下文保持 256 KiB。

主代理保留已有工作区改动，没有提交、推送、发布或启动新服务。三名 Luna 分别负责日志/备份、模型恢复、等待工具；主代理负责循环/完成验证、文档、整合及 Windows 结束标记修正。按 eri-comment-maintenance 核对本轮注释：说明了证据只保证来源、标记跨块过滤、流空闲时限与总时限的区别，没有把模型结论写成独立部署验收。

## 实际检查

Go 工具：`D:/develop/Go/bin/go.exe`。命令工作目录：`E:/WORK/lulu/deploypilot/client`。测试进程移除了 `BAILU_API_KEY`，没有调用收费模型。

| 结论 | 验证方式 | 结果与局限 |
|---|---|---|
| 防重复、真实验证引用及等待接入通过 | `go test ./internal/agent -run 'TestRepeatGuard\|TestFinishRequiresReal\|TestFileChangeInvalidates\|TestAgentPausesRepeated\|TestWaitForCanFinish\|TestFailedCommandIsNotVerification\|TestAgentHasNoStepLimit' -count=1 -timeout=30s` | 主代理实际执行，退出码 0，3.306s。覆盖无证据/伪造编号/失败命令拒绝、修改后证据失效、第四次重复暂停、实际 HTTP 204 等待后成功完成。原始输出在本轮工具记录。 |
| 模型接口包与入口配置通过 | 首次 `go test ./internal/agent ./internal/llm ./internal/server ./cmd/deploypilot -count=1 -timeout=120s` | LLM 包 4.671s、入口包 0.709s 通过；该整条命令退出码 1，不能算整体验证通过。首次失败及其他包结果保存在 `.probe-tmp/quick-agent-tests.log`。 |
| Agent 与服务端受影响包回归通过 | 修正后 `go test ./internal/agent ./internal/server -count=1 -timeout=120s` | 退出码 0，Agent 23.941s，server 14.084s。记录 `.probe-tmp/quick-agent-retest.log`。首次旧测试将备份误认临时文件，已按新合同修正。 |
| 最终 Windows 标记及终端关联行为通过 | `go test ./internal/server -run 'TestOutputMarker\|TestSharedShell' -v -count=1 -timeout=45s` | 退出码 0，7.022s。5 项通过：长输出中段保存、内部标记不进终端历史、跨块过滤、目录/环境/人工续接、历史恢复、退出与取消。记录 `.probe-tmp/quick-agent-pty-retest.log`。此检查发生在最终显示过滤改动后。 |
| 三平台最终构建通过 | `CGO_ENABLED=0`，分别设置 `GOOS/GOARCH` 为 windows/amd64、linux/amd64、linux/arm64，执行 `go build -ldflags '-X main.version=2026.10.30-quick-agent' -o <产物路径> ./cmd/deploypilot` | 三条命令均退出码 0。产物位于 `.probe-tmp/quick-agent-reliability/`，附 SHA-256 `checksums.txt`；未覆盖已有 dist。 |
| 差异无空白错误 | `git -c core.safecrlf=false diff --check` | 退出码 0；不代表功能或全仓测试通过。 |

## 失败与修正记录

首次服务端进程在约 29 秒后以 Windows 原生异常码 `0xc0000374` 退出，没有 Go 堆栈。源码可确认新增标记在赋值前被引用，导致等待错误；修正取值和输出完成路径后，单项及服务端包检查通过。ConPTY 原生异常的内部原因未独立确认，不把一次修正后的通过推成通用稳定性保证。

尝试使用终端标题控制序列隐藏标记时，实际 Windows 初始化用例等不到该序列而超时；已撤去该做法，最终使用随机文本标记并在服务端过滤，控制序列方案不在最终代码中。最终记录已由后续成功检查覆盖。

并行施工期间出现过未完成文件的编译错误，没有把它们当作有效的功能失败测试证据。

## 使用边界与未验证

- 未运行真实多小时部署、线上模型故障/限流或费用评测；没有证明部署成功率提高了多少。
- Linux 两架构仅完成交叉构建，未在 Linux 真机运行；Windows 的真实 PowerShell/ConPTY 路径已按上表检查。
- 未重复跑无关浏览器套件、未做全仓压力测试。前端未新增专门的等待卡片，使用现有活动/状态/工具记录显示。
- 完整日志指该次命令执行期间收到的输出；已脱离的后台服务后续输出仍需读取服务自己的日志。Windows 日志包含实际终端字节，可能有控制字符；Unix 创建权限为 0600，Windows 权限继承所在用户目录，未单独核验 ACL。
- 日志与备份目前不自动清理；备份覆盖 write/edit，不覆盖任意 Shell 修改或 append。
- `wait_for` 的日志首次仅扫描最近 1 MiB；同等或更大尺寸的替换文件未做文件身份识别。PID 消失不代表任务成功，URL 2xx/3xx 也不代表业务功能已验收。
- finish 检查真实来源和执行状态，不能独立判断某条检查是否覆盖整个部署目标。新命令/文件修改及新一轮续跑会使旧验证失效。

## 产物

- `.probe-tmp/quick-agent-reliability/deploypilot-windows-amd64.exe`
- `.probe-tmp/quick-agent-reliability/deploypilot-linux-amd64`
- `.probe-tmp/quick-agent-reliability/deploypilot-linux-arm64`
- `.probe-tmp/quick-agent-reliability/checksums.txt`

## 后续复核：复杂任务与前后端交互

2026-10-30，主人追问复杂任务是否相对稳定、前后端互动是否仍有问题。本次只核对当前六项首版及其关联交互，不修改实现，不把基础联调通过扩大为复杂任务稳定性认证。

| 结论 | 验证方式 | 结果与局限 |
|---|---|---|
| 前端基础状态与事件检查通过 | `npm --prefix web test` | 退出码 0，33/33，无跳过；`.probe-tmp/quick-agent-web-unit.log`。 |
| 浏览器任务交互检查通过 | 在 web 目录运行 `node tests/task-interactions.browser.cjs` | 退出码 0，18 项通过；真实 Chrome，模拟 API/WS。覆盖草稿、历史切换竞态、提交确认、重连不重发、IME、停止清理及恢复；`.probe-tmp/quick-agent-ui-interactions.log`。 |
| 真实后台与浏览器的基础联调通过 | `npm --prefix web run test:integration` | 退出码 0，Go 测试 10.784s，浏览器用例 6.50s。真实 Go HTTP/WS 与 Chrome、模拟模型/执行器；覆盖丢失接受回执不重发、运行中刷新、同会话补充、已用链接及认证过期。`.probe-tmp/quick-agent-frontend-integration.log`。该测试没有执行真正部署，也未模拟所有新增工具的完整 UI 流程。 |

仍存在的缺口：

1. **长时间无事件的任务会受会话过期影响（静态推断，未实等 30 分钟）。** auth/auth.go:74 默认会话空闲 30 分钟过期；server/task_history.go:352 周期检查 owner 会话，无效则取消任务。新 wait_for 在 agent/agent.go:430–443 只发送开始、结束或错误事件，等待中没有周期进度；前端计时只更新本地 React 状态，WebSocket ping/pong 不续认证。因此没有其他输出或人工操作的 30 分钟以上等待会进入取消路径。此行为与现有认证边界有关，需要明确协调，不能直接把认证超时移除。
2. **等待工具显示不完整（源码所示）。** web/src/Timeline.tsx 的 toolLabels/toolIcons 尚无 wait_for，网页回退显示原始工具名；agent/agent.go 的等待结果只返回给模型，未发送 output 事件，独立工具卡不展示最后观察到的 HTTP 状态/日志匹配结果，等待期间也无进度。成功 finish 引用该记录时，结果可以出现在最终总结中。
3. **复杂任务的恢复与人机协作仍有限。** 仍只允许继续最近结束任务，模型上下文仅在内存，进程重启后不能续跑（server/task_history.go:296–297）。运行中插话与 ask_operator 不在本次六项首版范围内，尚未实现。

当前判断：短故障恢复和基础任务交互有实际检查支持；未有 3–5 个真实长任务的对比数据，不能量化成功率或确认长任务已稳定。上述缺口未在本次复核中修复。

## 追加修复结果：会话持有、等待进度与链接复开

主人在上述复核后明确授权修复。两处缺口现已处理，替代上一节“未修复”的当前状态；最初六项及历史失败记录保留供回查。

实现：

- 有效会话在任务启动前由 `HoldSession` 持有，启动失败释放；运行和清理完成后幂等释放并重新开始完整空闲期。过期会话不能复活；撤销、隧道重建或更换配对使旧会话立即无效，旧任务释放不会影响新会话。
- `wait_for` 发出中文等待活动，状态变化或约每 10 秒更新已等时间、时限和最近观察；成功时把实际条件结果发到工具输出。网页显示“等待条件”，工具卡和全局活动均可显示进度；前后端只保留同一调用的最新连续等待活动，刷新可恢复。
- 一次性配对令牌和原有 Cookie 保护未改变：同浏览器有效 Cookie 可以重开原链接，新浏览器不能重用已消费令牌。运行中会话持有也没有取消单任务限制。
- 注释检查补充了会话持有/撤销边界、同步进度回调节流含义；没有声称 ping/pong 会续认证。

| 结论 | 验证方式 | 实际结果与局限 |
|---|---|---|
| auth/Agent/server 受影响包通过 | 主代理 `go test ./internal/auth ./internal/agent ./internal/server -count=1 -timeout=120s` | 退出码 0；auth 1.462s、agent 26.366s、server 18.748s。包括会话时间推进/撤销/陈旧 release、静默任务跨短空闲时限、等待回调/取消/输出顺序和历史合并；`.probe-tmp/wait-progress-go-tests.log`。 |
| 前端状态与基础界面通过 | 主代理 `npm --prefix web test` | 退出码 0，34/34，无跳过；新增等待活动归并、调用关联、任务隔离和实际输出检查；`.probe-tmp/wait-progress-web-unit.log`。 |
| 既有真实后台浏览器联调通过 | 主代理启用 `DP_BROWSER_INTEGRATION=1` 后运行两个集成测试 | 原有 TestFrontendBrowserIntegration 通过，4.20s；`.probe-tmp/wait-progress-browser-integration.log`。该组合命令首次整体退出码 1，原因是下行新增测试的异步断言，不能将该次组合命令记为全部通过。 |
| 新真实等待联调通过 | 主代理 `DP_BROWSER_INTEGRATION=1 go test ./internal/server -run '^TestWaitForFrontendBrowserIntegration$' -v -count=1 -timeout=75s` | 退出码 0；Go 包 10.154s，浏览器用例 8.04s。真实 Chrome + Go HTTP/WS，本地模型/执行器替身，真实本地 HTTP 503→204。静默超过测试会话 4 秒空闲期后刷新仍运行并恢复进度；实际 HTTP 204 展示、停止后控件恢复、原链接同浏览器复开、新浏览器无法重用令牌均通过；`.probe-tmp/wait-progress-browser-retest.log`。 |
| 最终前端构建通过 | `npm --prefix web run build` | 退出码 0；当前 JS `index-DaSX3TdM.js`，记录 `.probe-tmp/wait-progress-web-build.log`。 |
| 三平台构建与差异检查通过 | windows/amd64、linux/amd64、linux/arm64 分别 `CGO_ENABLED=0 go build -ldflags '-X main.version=2026.10.30-wait-progress' ...`；`git -c core.safecrlf=false diff --check` | 所有命令退出码 0。最终产物 `.probe-tmp/wait-progress/`，附 SHA-256 checksums.txt；构建包含上行最新前端资源。未启动、未发布。 |

Luna 另执行了 19 项模拟 API/WS 的浏览器交互检查并回报通过；主代理未独立复跑该套，本节主要结论采用上表主代理保留的原始结果。

新增测试首跑在停止后立刻检查执行按钮 enabled，早于后台清理确认而失败；改为等待 Playwright 的可操作状态后检查，产品代码未为该断言改变。另一个前端 mock 检查的初始历史选择不符合新建任务路径，由 Luna 修正 fixture 后重跑；未因此移除既有检查。

时间边界验证使用 fake clock 和缩短的会话时限，不是实跑 30 分钟或多小时部署。依然未测线上模型和 Linux 真机；任务重启后完整上下文恢复、运行中插话、ask_operator 仍在本轮之外。程序未启动，不声称现有用户入口已切换。

## 提交前清理与检查

关联 issue #1，分支 `chore/agent-reliability-cleanup`，起点提交 `9d5114c14a988966f9c68865c91338a42962f8bf` 与核查时的 origin/main 一致。

清理结果：删除无生产调用的终端 Agent 格式化器及其两个专测，停止向终端转发无消费者的 AgentEvent；任务事件、PTY 字节、控制状态和输入所有权保留。34 份历史文档、11 张旧界面截图由 docs/README.md 的固定 Git 版本索引承接，仍适用的界面要求整理到 docs/ui-guidelines.md。浏览器截图改写到忽略目录。

历史保全：对退出树的 45 个文件逐一确认固定 Git 对象存在，生成了该提交的文档 ZIP；当前工作树原件均已复制并比较 SHA-256，包括 5 张未提交 PNG，清单与原件在 `.probe-tmp/cleanup-preserve-20261030/`。代码改删前原件在 `.probe-tmp/cleanup-code-before/`。批量文件系统删除未执行，最终仅用 Git 对明确的已跟踪路径执行可回查的删除；本地忽略缓存、依赖、构建和原始日志没有清空。

| 结论 | 验证方式 | 实际结果与局限 |
|---|---|---|
| Go 检查通过 | 主代理 `go vet ./...` 与 `go test ./... -count=1 -timeout=120s` | 两条命令退出码 0；全部有测试包通过，PTY/webui 无测试文件。没有启用真实模型调用。日志 `.probe-tmp/cleanup-go-vet.log`、`.probe-tmp/cleanup-go-tests.log`。 |
| 前端单测通过 | 主代理 `npm --prefix web test` | 退出码 0，32/32；较上一轮减少的 2 项是随废弃 formatter 删除的专测。日志 `.probe-tmp/cleanup-web-unit.log`。 |
| 前端构建与三套浏览器检查通过 | 主代理 `npm --prefix web run test:browser` | 退出码 0；桌面/手机 workspace、19 项任务交互、5 项认证恢复全部通过。没有网页异常，新截图在 `.probe-tmp/ui-screenshots/`，Git 中不再产生截图修改。日志 `.probe-tmp/cleanup-web-browser.log`。 |
| 清理后真实后台联调通过 | 主代理 `DP_BROWSER_INTEGRATION=1 go test ./internal/server -run '^(TestFrontendBrowserIntegration|TestWaitForFrontendBrowserIntegration)$' -v -count=1 -timeout=90s` | 退出码 0，包 13.825s；既有联调 3.24s，新等待联调 8.22s。覆盖静默任务、刷新/回执丢失不重发、等待结果、停止、续聊和链接复开。模型/执行器为本地替身，无真实部署。日志 `.probe-tmp/cleanup-browser-integration.log`。 |
| 无旧代码/截图写入引用 | `rg` 核对源码、测试、脚本、README、当前文档 | 旧 formatter/转发方法及其测试列表引用为零；旧截图目录只出现在固定历史链接/取回示例，不再作为运行时写入路径。此结论覆盖仓内已查入口，不证明不存在仓外消费者。 |
| 差异与敏感内容检查 | `git diff --check`；本轮源文件凭证模式扫描 | 空白检查退出码 0；模式命中仅两处既有 QR/链接测试 fixture，没有发现待新增的真实凭证。模式扫描不能代替全面保密审计。 |

本轮只是清理、源码提交与测试准备，不发布 Release、不合入主干。真实长部署效果、Linux 真机行为和模型选择评测仍待下一步实际测试。

## 追加：普通问候误报失败与品牌图标

主人随后明确要求直接发布；v0.4.4 已发布（881f9db）。用户实际测试截图显示：自然语言问候被无工具回复提醒强制转入 finish，成功验证门禁又不接受无证据成功，最终 finish(false) 被网页显示为部署失败。源码调用链支持这一原因；未读取或推测用户的私有模型请求。

普通非空回复在尚无工具活动时直接结束本轮，以 reply 结果记录且不声称部署成功；有过工具活动（包括续聊历史）的任务保留 finish 要求。finish 可使用 outcome=needs_input 表达具体提问，只接受 success=false 且不得附验证编号。默认 finish(false) 仍是失败。蓝色品牌 mark 从 a4a30e2 的原始资源恢复，SHA-256 为 289a2089fb020c1624cf0657fe7a2fdfd2a0310ef892475bbad770592b251173，旁边使用 DeployPilot 文字。

| 结论 | 验证方式 | 实际结果与局限 |
|---|---|---|
| Agent 结束语义和验证门禁通过 | Luna 定向测试；主代理整个 `go test ./internal/agent -count=1 -timeout=90s` | 两者退出码 0；整包 23.463s。覆盖普通回复、空回复、执行后缺 finish、Resume 读取历史、needs_input 参数冲突和真实验证记录。整包日志 `.probe-tmp/conversation-agent-tests.log`；定向测试在 Luna 工具记录中。 |
| 受影响包静态检查通过 | 主代理 `go vet ./internal/agent ./internal/server` | 退出码 0，无诊断，日志 `.probe-tmp/conversation-go-vet.log`。 |
| 前端单测与构建通过 | Luna `npm test`；主代理 `npm --prefix web run build` | 退出码 0，32/32；构建包含品牌 PNG。单测在 Luna 工具记录中，构建日志 `.probe-tmp/conversation-web-build.log`。 |
| 三种最终显示保留不同含义 | 主代理 `node web/tests/workspace.browser.cjs` | 退出码 0，普通 reply 不出卡、needs_input 中性卡、无 state 的失败红卡检查通过；桌面/手机与原有操作检查通过。日志 `.probe-tmp/conversation-workspace-browser.log`。 |
| 实际网页链路可用 | Luna `DP_BROWSER_INTEGRATION=1 go test ./internal/server -run '^TestConversationFrontendBrowserIntegration$' -count=1 -timeout=90s -v`，主代理核对原始日志及两张截图 | 退出码 0，包 5.323s、Chrome 3.13s，一次通过；问候仅一轮模型请求，无失败卡；提问刷新恢复并可继续，实际失败仍红，清理后输入可执行，品牌图片真实加载。日志 `.probe-tmp/conversation-real-backend.log`；截图 `.probe-tmp/ui-screenshots/conversation-desktop.png`、`conversation-mobile.png`。 |

联调使用本地脚本模型和执行器替身，验证的是程序与网页路径，没有调用线上模型或真实部署。源码修复准备提交推送并按已有发布授权构建 v0.4.5；发布位置与下一步以 issue #1 为准，不把本地测试说成用户正在运行的进程已更新。

## 追加：SSE upstream_busy

使用用户给出的原始繁忙错误载荷构造本地 SSE 响应。改前主代理回归测试退出码 1，直接得到相同 `model stream error`，证明流内繁忙没有走退避。修复只识别结构化 code=upstream_busy，沿用现有 8 次上限和可取消等待；不放宽其他流错误，不自动切换模型。错误耗尽后给出中文恢复建议和固定 code，避免转发上游原始消息/元数据。

| 结论 | 验证方式 | 实际结果与局限 |
|---|---|---|
| 繁忙恢复、上限和取消通过 | Luna `go test ./internal/llm -count=1`，主代理读取原始日志与差异 | 退出码 0，5.748s。覆盖同请求重试、局部文本/工具丢弃、8 次耗尽、回调取消、未知流错误和 401 不重试、最终错误不带密钥。日志 `.probe-tmp/upstream-busy-llm-tests.log`。 |
| 已完成命令不重放，半截工具不执行 | 主代理 `go test ./internal/agent -run '^TestUpstreamBusyRetriesOnlyCurrentTurn$' -count=1 -timeout=25s` | 改前退出码 1；改后退出码 0，6.347s。安装只调用一次，繁忙回复中的 write_file 未执行，重试请求与原请求完全一致，之后健康检查与成功 finish 正常，等待状态可见。日志 `.probe-tmp/upstream-busy-agent-red.log`、`upstream-busy-agent-green.log`。 |
| Agent 整包与静态检查通过 | 主代理 `go test ./internal/agent -count=1 -timeout=90s`、`go vet ./internal/llm ./internal/agent` | 两者退出码 0，Agent 包 27.430s。日志 `.probe-tmp/upstream-busy-agent-tests.log`、`upstream-busy-go-vet.log`。 |

没有请求 BAILU AI 真实服务，不能确认上游恢复时间或当前容量。用户随后指出输入文字慢，当前保留此补丁，与前端输入性能修复一起构建发布；发布位置以 issue #1 为准。

## 追加：输入响应与终端历史落盘

用户明确反馈为键入文字回显慢，不是输入被禁用。代码核对发现：每次草稿更新会重新渲染全部日志行；终端快照的文件操作会占用 PTY 读取/广播共用的锁。前者隔离为 memo 日志子树并缓存事件派生值，自动高度不再使用 layout effect；后者以 persistMu 串行快照和写盘，历史锁只用于短暂取值和复制。定时任务先等持久化锁再清待处理标记，避免慢盘排队累积；读取 ready 文件后检查 CWD 未被 Run 更新，避免旧值覆盖。

| 结论 | 验证方式 | 实际结果与局限 |
|---|---|---|
| 本机有界历史负载下键入工作减少 | Luna 在同一 Chrome fixture 优化前/后各构建、执行 `node tests/workspace-input-perf.browser.cjs`，200 条各 2 KiB 文本，键入 100 字符 | 命令均退出码 0，字符/行数均为 100/200。基线总 421ms，input 处理均值 1.031ms、p95 1.40ms；优化后总 373ms，均值 0.808ms、p95 1.30ms。原始输出保留在 Luna 本轮工具记录。仅一次本地对照，不能断言用户现场大延迟由此造成或已消失；早先 1.6MiB 超出真实任务上限的观测不作为结论依据。 |
| 草稿、中文输入法、焦点与自动高度保留 | Luna 优化后一次 `node tests/workspace.browser.cjs` | 退出码 0，既有桌面/手机、输入高度/focus/IME 等检查通过；输出在 Luna 工具记录。 |
| 慢盘不再占实时历史/广播锁 | Luna `go test ./internal/server -run 'TestSharedShell(RestoresDirectoryAndHistory|PersistenceDoesNotHoldHistoryLock)$' -count=1 -timeout=30s`，主代理核对差异与原始日志 | 退出码 0，最终 3.537s。人为阻塞 snapshot writer 时追加、订阅和广播可继续；解阻塞后关闭完成，最新 CWD/输出落盘；原历史恢复测试通过。首轮 fixture 的订阅顺序错误已修正，日志保留全过程：`.probe-tmp/terminal-persist-tests.log`。不是用户磁盘耗时测量。 |
| 前端单测与最终构建通过 | 主代理 `npm --prefix web test`、`npm --prefix web run build` | 退出码均 0，单测 32/32，构建包含品牌图标。日志 `.probe-tmp/input-web-unit.log`、`input-web-build.log`。 |
| 整合后实际网页链路通过 | 主代理一次运行三项 `FrontendBrowserIntegration`、`WaitForFrontendBrowserIntegration`、`ConversationFrontendBrowserIntegration` | 退出码 0，包 17.274s，三项分别 4.02s、8.07s、2.93s；覆盖真实 Go/WS/Chrome、刷新/续聊/停止/等待/认证与普通问答。模型/执行器为本地替身。日志 `.probe-tmp/input-browser-integration.log`。 |
| 受影响 Go 包静态检查通过 | 主代理 `go vet ./internal/llm ./internal/agent ./internal/server` | 退出码 0，无诊断。日志 `.probe-tmp/input-go-vet.log`。 |

终端字符仍来自目标 Shell 的真实回显，需要网络往返。本次没有测量用户设备、浏览器或隧道 RTT，不增加假本地回显、不移除屏幕阅读支持、不因性能反馈改变任务输入锁。准备将繁忙恢复和两处性能改进一起发布 v0.4.6，当前正在运行的用户实例未被替换或重启。

## 追加：工具检查后不再强制 finish

主人指出 hello.py 检查已得出结果，却附带“未调用 finish，任务未确认完成”。先前只允许无工具对话自然结束，确实把工具使用与失败判定错误绑定。现撤去该门槛：所有完整且非空的自然回复都中性结束本轮，不需要按命令类型猜测是否只读；明确成功仍由 finish(success=true) 关联真实证据。中性 reply/needs_input 的 JSON 不再带任务 success 字段；旧失败记录及显式失败仍保持。连续空回复才提醒一次，之后说明未取得有效答复，不再说缺 finish。

| 结论 | 验证方式 | 实际结果与局限 |
|---|---|---|
| Agent 回复与验证语义通过 | Luna `go test ./internal/agent -count=1`，主代理读取日志与差异 | 退出码 0，28.376s。覆盖工具使用/Resume 后自然答复、hello.py 读取后解释、重复空回复、JSON 中性标记与既有成功验证/真实错误检查。日志 `.probe-tmp/natural-reply-agent-tests.log`。 |
| 实际网页中的 hello.py 场景通过 | Luna `DP_BROWSER_INTEGRATION=1 go test ./internal/server -run '^TestConversationFrontendBrowserIntegration$' -count=1 -timeout=90s -v`，主代理核对原始日志 | 一次通过，退出码 0，包 5.836s、Chrome 3.56s。临时目录实际 read_file 内容传入模型；正常回答无失败卡、不再请求 finish，刷新后保留、输入可继续；原问候/补充信息/真实失败场景也保留，共 5 次模型请求（既有 3 次加读取/回答 2 次）。日志 `.probe-tmp/read-reply-browser.log`。 |
| 受影响包静态检查通过 | 主代理 `go vet ./internal/agent ./internal/server` | 退出码 0，无诊断，日志 `.probe-tmp/natural-reply-go-vet.log`。 |

模型为脚本替身，文件使用隔离的临时目录，未读取用户实际工作目录、未请求付费模型。前端源代码没有变化，沿用 v0.4.6 的构建资产；联调实际验证它兼容中性 JSON。准备提交推送并按既有授权发布 v0.4.7，运行实例不由本次源码操作自动替换。
