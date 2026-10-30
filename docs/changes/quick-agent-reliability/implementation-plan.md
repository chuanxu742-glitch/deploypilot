状态与批准范围见本目录 README。

没有 SDD；按本轮明确的六项建议做最小实现，不做架构重写。使用 eri-implementation，旧代码限本需求最小修改，保留原有工作区差异。

- Luna A：agent/executor.go、command_log.go、backup.go、server/shared_shell.go；保持 Executor 接口，完整日志落私有文件，截短结果附路径，write/edit 在替换前备份，失败不继续覆盖。
- Luna B：llm/client.go 与流超时辅助文件；连续无数据超时后终止本请求并归类可重试中断，429/5xx 做有限、可取消的分钟级退避。
- Luna C：agent/tools.go、tool_validation.go、wait_for.go；支持 URL、日志、进程等待，增加 finish verification_call_ids 参数。
- 主代理：agent/agent.go 与 reliability_guard.go，接入 wait_for；相同调用与结果三次提醒，继续重复暂停；成功 finish 引用本轮真实成功命令/等待的编号和实际结果；更新 README 与本记录。

验证：每模块补少量关键成功/失败/取消检查；各自定向测试。接口接齐后一次运行受影响 Go 包；构建 Windows 本机与 Linux amd64/arm64 产物。失败仅针对相关处重测，不反复跑全仓、不跑无关浏览器测试。不修改运行实例，不公开发布。

## 追加：长等待与网页进度

没有新增 SDD；根据专项核对发现和主人明确授权最小修复两处缺口，继续 eri-implementation，不扩大到机器记忆、运行中插话或新的登录方式。

- Luna A：auth/auth.go 增加有效会话持有/释放；server/task_history.go 将任务生命周期绑定到会话持有，失败分支释放，保留撤销检查；连续等待活动只保留最新条。fake clock 验证长时间运行、结束后空闲恢复、撤销和过期令牌不可复活。
- Luna B：agent/wait_for.go 增加兼容现有调用的进度回调；agent.go 用 waiting 活动显示等待对象、时间、最近观察，结束前发实际 output。验证回调、取消和成功结果。
- Luna C：前端等待工具标签/进度展示，连续同调用等待活动替换，工具卡按 callId 关联进度，保留实际结果。执行 npm test 和一次关联浏览器检查。
- 主代理：补一条真实 Go HTTP/WS 与 Chrome 的等待联调，覆盖短认证期限下静默等待、刷新、成功结果、停止、同浏览器原链接复开及新浏览器不能重用已消费令牌；更新入口说明与验证记录，最终构建三平台。

检查只针对受影响范围；新增浏览器 fixture 使用本地模型替身、真实本地 HTTP 条件，无真实部署/模型收费。不修改Cookie保护属性，不取消单会话约束；未提交、未发布、未启动用户运行实例。

## 追加：清理并提交 GitHub

主人要求“清理废弃的代码，图片，文档等文件，提交git和GitHub，然后我们去进行测试”。对应 issue #1，分支 `chore/agent-reliability-cleanup`，起点 `9d5114c14a988966f9c68865c91338a42962f8bf`；沿用已有工作副本，不覆盖或 stash 未提交工作。

- 删除已无生产调用的终端 Agent 格式化器及专测，停止向终端 WebSocket 转发被前端丢弃的 AgentEvent，保留任务事件流、PTY 输出、控制状态和输入锁定。
- 旧的 7 组事项文档/截图先核对 Git 对象、复制工作树原件并逐文件比 SHA-256，再退出当前树。固定历史索引由 docs/README.md 承接；仍适用的 UI 约定整理到 docs/ui-guidelines.md。
- 浏览器截图输出改到被忽略的 `.probe-tmp/ui-screenshots/`，保留截图检查；只清理已跟踪且可恢复的过时源文件，不清最新构建、日志、忽略缓存或依赖目录。
- 清理后运行一次现有 Go 检查、前端测试/构建和关联浏览器检查，确认截图不会重新进入 git status。沿用已取得的真实等待联调证据，若终端事件变动影响其覆盖则补跑。
- 提交当前已授权的完整实现及清理到本地 Git，推送 origin 并开 PR；不强推、不发布 Release，不自行合入主干。PR 保留实际测试边界，便于下一步真实部署测试。
