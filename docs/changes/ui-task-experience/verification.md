状态与批准记录见本目录 README。

# 验证记录

2026-10-29，亲自执行，Windows amd64、Go 1.27.0、Node.js 26.5.1、Chrome 无头浏览器。验证对象为本次未提交工作区。

## 证据表

| 结论 | 验证方式 | 结果与局限 |
|---|---|---|
| 前端状态与日志边界通过 | `npm --prefix web test` | 退出码 0，6 项通过、0 失败。源码在 `web/tests/agent-state.test.ts`；实际输出在本会话命令记录。 |
| 前端类型检查和生产构建通过 | `npm --prefix web run build`，也由 `test:browser` 调用 | 退出码 0，42 个模块构建完成。原始构建与浏览器输出在 `.probe-tmp/browser-check.txt`。 |
| Go 静态检查通过 | `cd client; go vet ./...` | 退出码 0，见本会话执行记录。未执行 race detector，不以普通并发测试声称无数据竞争。 |
| Go 回归通过 | `cd client; go test -json ./...` | 退出码 0，57 项通过、0 失败、4 项跳过，6 个有测试的包通过。原始输出 `.probe-tmp/go-tests.jsonl`。 |
| 本机程序可以构建 | `cd client; go build -o ../.probe-tmp/deploypilot.exe ./cmd/deploypilot` | 退出码 0。只验证编译，未通过真实隧道运行此二进制。 |
| 桌面和手机交互通过 | `npm --prefix web run test:browser` | 退出码 0；真实 Chrome 加载生产页面，模拟 API 和 WebSocket。覆盖 1440×1000、390×844；无页面异常和横向溢出。测试保存在 `web/tests/workspace.browser.cjs`。 |
| 开发模式的一次性配对无重复请求 | `node .probe-tmp/pairing-check.cjs`，本地开发服务器 4174 | 退出码 0；React StrictMode 下只配对一次，成功后移除 hash；首次模型设置正确禁用空保存与模型拉取。模拟端点，非真实隧道。 |

## 验收条件对应

| 验收条件 | 由什么验的 | 结果 |
|---|---|---|
| D1 桌面双面板、手机页签且切换保持连接 | 浏览器测试检查两个面板的可见性和终端连接次数 | 通过；截图 `desktop.png`、`mobile.png`。 |
| D2 显示运行与停止状态、清理前不可重新执行 | `applyAgentEvent` 两条测试；浏览器停止按钮与清理确认检查 | 通过。 |
| D3 工具错误不误判结束、清理前拒绝新任务 | 前端错误状态测试；Go `TestAgentStopWaitsForCleanupBeforeRestart`、`TestAgentCompletionAcknowledgesCleanupAndAllowsRestart` | 通过。Go 测试可控制退出时机，无须依赖 Shell 清理快慢。 |
| D4 分类、折叠、停止跟随和容量限制 | 浏览器展开输出、上翻与返回最新；前端尾部截断、单条上限和记录数量测试 | 通过。没有做吞吐量或长时间性能基准，不声称性能提升百分比。 |
| D5 模型弹窗、当前模型、加载保存反馈和重复提交保护 | 浏览器获取列表、保存后重新打开核对模型；运行中禁止修改；源码同步占位和 disabled 检查 | 当前模型与反馈通过；人为快速重复点击未单独压测。 |
| D6 关闭连接与尺寸监听 | Go `TestAgentDisconnectCancelsRun`；浏览器核对手机切换后终端仍为一次连接、行列数缩小且大于零 | 断线取消和尺寸调整通过；组件卸载清理已检查源码，未单独增加 React 卸载测试。 |

## 改前改后对照

- 前端针对原有错误结束判断和无界日志的测试首次为 1 通过、5 失败；修复后 6 通过。
- Go 生命周期测试在后端实现前出现预期失败：拒绝启动后收不到空闲确认、首条事件没有运行状态。实现后通过。
- 完成测试最初依赖真实机器环境探测，偶发超过 5 秒读取期限；改为注入固定环境执行器，排除与生命周期无关的网络和 PowerShell 时间差，未放宽被测生命周期条件。

## 证据缺口与环境记录

- 4 项跳过分别是 `TestLiveAgentLoop`、`TestLiveListModels`、`TestLiveStreamChat`、`TestLiveToolCalling`；未使用真实 API Key 调用付费模型。
- 未验证真实 Cloudflare 隧道、Linux 运行环境和长时间弱网；本次浏览器测试使用模拟服务，不把它说成生产部署已验收。
- Go 自动下载工具链开始较慢；默认下载最终完成并用于成功的最终测试。期间备用缓存尝试出现编译环境错误，额外 curl 下载超时，均没有替代最终成功结果，也没有修改项目的 Go 版本要求。
- `npm ci` 报告原有依赖存在 2 项审计提示（1 moderate、1 high）；本次没有升级与界面任务无关的运行依赖。
- 注释按 `eri-comment-maintenance` 核查，说明集中在服务端清理确认的锁顺序、配置快照、前端后台标签页输出上限及开发模式配对共用请求处。
- 没有创建提交、推送、PR 或公开发布。
