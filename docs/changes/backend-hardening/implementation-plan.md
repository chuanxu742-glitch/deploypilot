# 实施计划

## 依据与工作模式
没有独立 SDD；依据本目录 requirements.md B1–B8、上轮检查的复现结果与用户本轮修复授权。使用 eri-implementation，先加入期望正确行为的失败测试，再改实现。允许在本范围内最小修改旧调用路径，不进行无关重构，不建提交，因此无需为提交新建 issue。

## 文件与次序
1. `internal/agent/agent.go` 与任务回归测试：每个工具前后检查取消；拒绝不完整模型结果；工具失败发出错误事件。
2. `internal/auth/auth.go`、`internal/server/{agent,terminal,server}.go` 与新增会话守护：服务端权威到期，浏览器 session cookie；每条消息检查身份，守护关闭过期连接；服务级任务占用一直保持到清理完成。测试过期、续期、撤销、重连拒绝与清理后恢复。
3. `internal/agent/{executor,proc_linux,proc_other}.go` 与 `proc_windows.go`：平台运行入口；Windows 在开始执行前加入 Job Object（操作系统管理的一组关联进程），取消时结束整个作业，正常成功不杀后台服务；真实隐藏进程测试。
4. `internal/llm/{client,stream}.go` 与协议测试：完成标记与 finish_reason 校验、流错误上报、响应容量边界、部分流不重试、调用身份校验。
5. `internal/agent/tools.go` 与工具校验文件/测试：严格解码、必需字段及值域检查，所有既有 6 类工具保持原名；平台工具说明修正为当前 shell。
6. `internal/agent/context.go` 与测试：上下文容量控制与完整轮次压缩，保留初始目标。采用已说明的本地压缩假设，不额外请求模型。`cmd/deploypilot/main.go` 与服务端 Options 传入 `DP_CONTEXT_BYTES`，默认 128 KiB，允许 16 KiB–2 MiB；无效配置在启动服务前报错。
7. `README.md`、本目录验证记录；必要时 go.mod 将已使用的 x/sys 明确列为直接依赖，不引入额外第三方运行依赖。

## 检查安排
每片运行对应包和直接调用方包的测试，并记录红/绿结果。结束后统一执行 Go 全套测试、go vet、本机与 Linux amd64/arm64 交叉编译。全套目前约数秒，不在每个单行修改后重复跑。HTTP、WS、SSE 使用本地模拟服务；Windows 子进程用实际隐藏进程。已有前端协议保持，完成后跑前端状态测试核验兼容；未变更前端布局不重做视觉验收。真实模型和真实隧道不调用。

## 对照与边界
每片对回 B1–B8；压缩后先重读本目录 README 与计划。保留现有未提交改动、原有模型设置快照行为、工具串行执行；不添加长期记忆、并行执行框架或临时双实现。公开发布、现有会话重启不在本次授权范围。
