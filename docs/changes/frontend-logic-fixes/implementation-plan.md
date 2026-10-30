状态与批准记录见本目录 README。

## 依据

没有 SDD。依据本会话已确认修复范围、requirements.md 第 1–8 项与 `.probe-tmp/frontend-audit/report.md`，接续既有前端任务稳定性要求。

## 工作模式

使用 eri-implementation，先以行为测试复现，再修复并验证。沿用既有前端实施计划允许在范围内整理组件的授权；仅为本次缺陷修改已有调用路径，保留其他未提交工作。不创建提交、推送或公开发布。

## 对照设计

每组回看 8 项原始触发与不动范围；分组独占可写文件。主代理核查交回的实际 diff、接口合同、原始红绿结果，再独立运行关键用例和仓库回归。不会用子代理成功摘要代替证据。

## 改哪几个文件、每处怎么改

- web/src/Agent.tsx：历史请求序号/取消、按会话保存草稿、待确认发送恢复、全局运行状态与所选任务分离、与错误对应的重试、明确补充消息语义。
- web/src/agent-state.ts：必要事件类型和纯状态辅助，保留 user 续聊渲染。
- web/src/api.ts：受保护 HTTP 401 统一通知，带超时的断线会话探测、有效 Cookie 优先于旧令牌；TaskRecord 增加可选 requestId。
- web/src/App.tsx：认证错误信息与重试入口，保留品牌和布局。
- web/src/Terminal.tsx：断线后校验会话，明确过期才回配对页，网络未知保留重连能力。
- client/internal/server/agent.go：start 请求携带可选 requestId，拒绝沿用 error 并回显请求身份。
- client/internal/server/task_history.go：仅在接受新建/续聊后保存最后接受的 requestId，snapshot/history 可核对；运行状态有明确任务时带 taskId。
- web/tests/auth-recovery.test.ts、auth-recovery.browser.cjs（按需要）：认证前后路径测试，不依赖收费服务。
- web/tests/task-interactions.browser.cjs、task-interactions.test.ts（按需要）：当前 8 项前端行为的正式回归，含乱序、丢包、迟到响应和草稿归属。
- client/internal/server/start_request_test.go：真实 Go WebSocket 接受、拒绝、续聊与重连确认。
- web/package.json：把新增正式测试纳入正常检查入口。
- README.md：同步已经实现的用户行为。

## 协作接口合同

- start 消息可带 requestId，已接受任务快照中的 task.requestId 回显该值；未携带的旧调用保持可用。客户端不自动重发命令，不把 socket.send 成功当接受。
- 拒绝沿用 type:error，并带原 requestId（如果提供），避免旧界面无法显示错误。
- 有明确归属的 run_state 携带 taskId；全局空闲不伪造历史任务身份。
- api.checkSessionAfterDisconnect(): Promise<boolean>：false 仅为明确 HTTP 401 且已通知会话过期；true 包括有效会话、网络错误或未知，允许正常连接恢复。

## 测试挂哪

每组先运行对应新行为测试、记录有效失败，再实现。各组完成其范围类型/单元检查，不并行改同一组件。集成后主代理运行 `npm --prefix web test`、新增浏览器测试、既有 `npm --prefix web run test:browser`、`go test ./... -count=1 -timeout=180s`、`go vet ./...` 与生产前端构建/本机构建。日志保留 `.probe-tmp/frontend-fixes/`。

重点接缝：HTTP401 与网络异常区分；有效 Cookie 与一次性旧令牌；WS首次快照、启动确认、重连后丢失/接受判断；历史GET响应与新建/发送；旧历史元数据与后台busy；按会话草稿切换；停止清理与多标签单任务限制。

最终以本机模拟模型、真实 Go 服务和 Chrome 检查接口联动，不调用真实付费模型或执行真实部署。先确认实际用户入口，再准备可安装构建；现有进程需要重启才能切换时，待构建和验证就绪后仅就中断现有会话取得确认。

## 不动什么

不改隧道、安装器、命令权限和部署知识；不实现队列/运行中指令干预/权限模式/文件差异预览；不重放不确定的部署指令；不覆盖用户既有未提交修改，不中断当前程序，不推送或公开发布。
