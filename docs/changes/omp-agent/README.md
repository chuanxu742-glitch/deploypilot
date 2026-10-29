# 参考 OMP 的 Agent 小幅优化

2026-10-29，用户要求“参考 omp 来简单优化一下我们的 agent”。目标仓 E:/WORK/lulu/deploypilot。已确认本机 `omp --version` 为 18.13.2，参考 can1357/oh-my-pi 同版本提交 e996e1c7ac476f60e02b8ed9764cf30fa8c00a8e。仅参考实现思路，不引入 OMP 依赖或照搬框架。

范围：长命令输出保留头尾，压缩优先保留最近完整轮次，工具错误有完整结束事件与模型反馈，普通文字回复不自动算任务成功。保持现有串行执行、工具名称、模型接口和原始目标保护，不增加模型摘要请求。本轮不涉及固定域名、后台任务恢复、沙箱、长期记忆或自动回滚；这些仍是上轮识别的独立缺口。

来源：
- [OMP bash 工具](https://github.com/can1357/oh-my-pi/blob/e996e1c7ac476f60e02b8ed9764cf30fa8c00a8e/packages/coding-agent/src/tools/bash.ts)：将执行错误作为工具结果，并附带退出码、超时信息。
- [OMP agent-loop](https://github.com/can1357/oh-my-pi/blob/e996e1c7ac476f60e02b8ed9764cf30fa8c00a8e/packages/agent/src/agent-loop.ts)：保留明确的 isError，不将工具结果与错误状态混淆。
- [OMP compaction](https://github.com/can1357/oh-my-pi/blob/e996e1c7ac476f60e02b8ed9764cf30fa8c00a8e/docs/compaction.md)：保护近期上下文、按完整工具调用/结果切分，并在部分序列化策略中保留输出头尾。我们只移植小范围原则，不声称具备 OMP 的可检索历史或 artifact 系统。

完成判定改法来自本项目审查：让模型显式调用已有 finish；它仍是模型给出的结论，不冒充独立部署验收。

授权覆盖本轮小改，沿用 eri-implementation；最小修改旧代码，先补失败用例再实现。没有新增提交、远端操作或发布。备份/测试证据在 .probe-tmp/omp-agent-20261029/，该目录不入库。计划见 implementation-plan.md。

状态：本地实现与验证完成，无仍在运行的检查，不需要用户额外操作。106 个顶层 Go 测试、20 项前端测试通过；go vet、三平台构建通过。详细结果和未验证项见 verification.md。现有运行实例未替换。
