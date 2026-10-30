状态与批准记录见本目录 README。

## 证据表

2026-10-30，主代理亲自执行最终检查。对象是当前未提交工作树及其生产构建；保留本轮开始前已有的续聊和品牌修改。最后一轮前端源码哈希在构建/检查前后相同，见 `.probe-tmp/frontend-fixes/final-source-{before,after}.json`。

| 结论 | 验证方式 | 结果与局限 |
|---|---|---|
| 单元回归通过 | `npm --prefix web test` | 退出码 0，33 通过、0 失败、0 跳过；`final-unit.log`。 |
| 生产前端构建与类型检查通过 | `npm --prefix web run test:browser` 内先执行 `tsc -b && vite build` | 退出码 0；`final-browser.log`。 |
| 既有桌面/手机流程通过 | 同一命令执行 `workspace.browser.cjs`，截图重定向至临时检查目录 | 退出码 0；覆盖主题、布局、模型列表、IME、焦点、输入高度、复制、停止清理、刷新重连与历史读取；无页面错误。API/WS 为模拟服务。 |
| 新增任务交互回归通过 | 同一命令执行 `task-interactions.browser.cjs` | 18/18；含迟到请求、草稿归属、首快照、发送确认、晚到确认、明确拒绝、异步错误与重试。日志 `final-browser.log`。 |
| 新增认证浏览器回归通过 | 同一命令执行 `auth-recovery.browser.cjs` | 5/5；包含 API401、不发4401的断线+session401、网络/503未知、旧配对链接；模拟接口。日志同上。 |
| 真实前后端联动通过 | `node web/tests/run-real-backend.cjs` 启动真实 Go HTTP/WS 服务并运行 Chrome | 退出码 0；故意丢首次接受快照，重连后 start 计数保持 1；刷新运行中任务不重放；补充消息复用同一任务且更新 requestId；旧链接和会话撤销可恢复。`final-real-backend.log`。模型与执行器为本地替身，不执行部署命令。 |
| Go 全量回归通过 | `go test ./... -count=1 -timeout=180s` | 退出码 0；所有有测试的包通过；`root-go-test.log`。需要 Chrome 的联调用例在普通 Go 全套中默认跳过，已按上一行单独显式执行。 |
| Go 静态检查通过 | `go vet ./...` | 退出码 0；无错误输出，`final-go-vet.log`。 |
| 本机可执行程序已准备 | `go build -ldflags '-s -w -X main.version=v0.4.1-frontend-fixes' ...`；`go version -m` 与 SHA-256 | 退出码 0；`dist/deploypilot-windows-amd64-frontend-fixes.exe`，元数据 `native-build-info.log`，构建输出 `final-native-build.log`。 |
| 安装目录中的待切换文件与构建一致 | 比对 `deploypilot.pending.exe` 和构建文件 SHA-256，并备份现有安装文件 | 两份新文件均为 `AA3BD30FEDB808C5891EAB88131A19E6B55D03D43A91220B2745636624CB7D3E`；原文件与备份一致。完整路径与哈希见 `artifact-hashes.json`。 |
| 正在运行的安装入口已切换 | 主人明确回复“切换吧”后执行哈希预检、替换、重启；检查实际进程目录、本机/公网 health、页面资源和 Chrome 界面 | 退出码 0；新进程 44076，版本 `v0.4.1-frontend-fixes`，启动目录仍为 `C:\Users\32536\`。旧进程及其隧道/终端子进程均已退出。实际安装文件哈希与已验证构建一致，JS/CSS 本机与公网均匹配；Chrome 配对页可见、0 页面错误、0 次提交配对令牌。证据 `installed-launch.json`、`installed-health.json`、`installed-public-health.json`、`installed-resource-check.json`、`installed-public-resource-check.json`、`installed-browser-check.json`。 |

本表日志除另写完整路径外，均位于 `.probe-tmp/frontend-fixes/`。完整命令退出码也保留在本会话工具记录中。

## 验收条件对应

| 验收条件 | 由什么验的 | 结果 |
|---|---|---|
| 1：仅应用仍有效的历史选择 | `stale history response cannot overwrite newer selection`、`new task and its draft invalidate a delayed history read`；发送会递增请求序号并取消旧请求 | 通过。 |
| 2：断线时保留提交并按身份核对，不自动重放 | 专测中的 unknown/accepted/late summary、人工编辑、切换至其他历史后确认、明确拒绝后两份文本切换；真实 Go+Chrome 丢首次回执 | 通过；不能证明接受时保持不确定，不把它解释为明确失败。 |
| 3：明确认证失效回配对，网络未知不误登出 | 认证单元5项、认证浏览器的 HTTP401/WS普通失败+401/network/503；真实后端会话撤销 | 通过。模拟普通关闭不是对真实公网握手故障率的测量。 |
| 4：有效会话优先于旧链接 | 单元旧令牌优先/失败兜底，浏览器旧链接不 POST pair，真实 Go+Chrome 复访已用令牌 | 通过。 |
| 5：草稿随会话保存与恢复 | 两个历史往返、未知/拒绝后新草稿、晚确认不盗取 B 的草稿；新建/恢复视图仍提供历史入口 | 通过。范围为当前工作台组件生命周期内的网页内存。 |
| 6：全局忙碌不篡改所选历史 | interrupted 旧任务+idle 通知、其他标签任务全局锁定；Go run_state 携带任务身份；App 回调使用全局运行状态 | 通过。 |
| 7：已有输入仍能识别补充消息归属，用户手动发送 | busy draft 专测；提示文字明确“结束后可手动…发送”；独立草稿不冒充其他标签运行任务的补充 | 通过。没有新增队列或自动发送。 |
| 8：错误重试操作与错误一致 | 历史失败只增加历史 GET；根代理新增“模型成功晚到”“历史成功晚到”两个交错用例 | 通过；两条新增用例在修复前均以重试按钮消失断言失败，修复后通过。 |

## 改前改后对照

- 主代理将正式任务测试指向保全的旧前端构建，复现了历史选择 A 覆盖 B、草稿为空、历史重试不增加 GET、旧任务被改成 finished、发送没有 requestId 等目标失败；原始输出 `root-task-baseline-red.log`，退出码 1。
- Luna 的旧 API 红测实际复现有效旧链接失败和 HTTP401 不通知；原始输出 `auth-red-baseline.log`。不是只把缺少新导出导致加载失败作为红阶段证据。
- 后端红测临时撤去 requestId 存储，live 确认断言失败；恢复实现后目标与全套通过，`protocol-red.log`、`protocol-green.log`。
- 主代理发现错误重试的交错响应缺口，先加两条用例，二者均失败（`0 !== 1`，按钮不存在）；`root-retry-race-red.log`。错误与重试在失败分支一起设置后，最终 18 项通过。

## 检查期间的失败与处理

- 首次统一浏览器检查出现默认 21px 字号。实测当时构建 CSS 不包含 settings-dialog/dialog-head，而源码随后已含完整规则，构建时间早于文件更新时间；重新用稳定源码构建后既有样式与行为检查通过。没有覆盖其他工作所做的样式修订，也没有放宽字号断言。失败记录 `root-browser.log`，随后通过记录 `root-browser-retry.log` 和最终记录 `final-browser.log`。
- 首次真实后台联调中，Go 测试为隔离审计设置的 USERPROFILE 被 Chrome 继承，Chrome 拒绝远程调试目录。仅将浏览器子进程恢复为原 Windows profile 根目录，Go 服务审计仍隔离；重跑通过。失败记录 `root-real-backend.log`，成功记录 `root-real-backend-retry.log`、`final-real-backend.log`。没有修改系统或用户 Chrome 配置。

## 证据缺口

- 安装后已验证真实 Cloudflare 公网入口的本次可达性、资源与页面渲染；未测试长期网络可靠性。未调用付费模型、执行真实部署或验证 Linux 真机终端，没有据此宣称所有部署场景通过。
- 草稿与待确认输入仍在组件内存中。网页关闭/刷新、明确认证失效引起组件卸载后不保留这些输入；接受后的后台任务及历史恢复是另外的既有能力。
- taskRecord 只保留该任务最新接受的 requestId。若较早提交一直未知，而同一任务又被其他标签续聊覆盖了最新 ID，客户端可能无法再证明旧提交是否接受；保留文本及不确定状态，不自动重发。这不是通用 exactly-once 执行系统。
- 安装进程已切换并核实际入口；一次性配对令牌留给主人，本轮未在用户新会话中配对或重新填写模型密钥。配对链接只在对话和 Git 忽略的私有临时记录中保留。
