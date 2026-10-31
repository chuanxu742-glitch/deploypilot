# 文档入口

- [使用、配置与构建](../README.md)：当前程序行为和开发命令。
- [界面约定](ui-guidelines.md)：当前仍适用的界面要求及检查位置。
- [长任务可靠性与本次清理](changes/quick-agent-reliability/README.md)：当前事项、实施记录和验证结果。

## 历史记录

2026-10-30 清理时，以下已完成实现的旧计划、验证记录和界面截图退出当前工作树。它们保留在固定提交 `9d5114c14a988966f9c68865c91338a42962f8bf` 中，不能作为当前默认值或运行状态说明。

- [Agent 循环](https://github.com/chuanxu742-glitch/deploypilot/tree/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/agent-loop)
- [后端加固](https://github.com/chuanxu742-glitch/deploypilot/tree/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/backend-hardening)
- [缺陷修复](https://github.com/chuanxu742-glitch/deploypilot/tree/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/defect-hardening)
- [前端交互修复](https://github.com/chuanxu742-glitch/deploypilot/tree/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/frontend-logic-fixes)
- [OMP 参考改动](https://github.com/chuanxu742-glitch/deploypilot/tree/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/omp-agent)
- [隧道恢复](https://github.com/chuanxu742-glitch/deploypilot/tree/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/tunnel-stability)
- [界面迭代与截图](https://github.com/chuanxu742-glitch/deploypilot/tree/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/ui-task-experience)

例如，可用 `git show 9d5114c14a988966f9c68865c91338a42962f8bf:docs/changes/ui-task-experience/requirements.md` 读取旧界面需求。历史测试结果只证明当时的版本；当前检查见当前事项的验证记录。

浏览器测试新生成的截图写入被 Git 忽略的 `.probe-tmp/ui-screenshots/`，不再覆盖仓内历史证据。清理前工作树中的 5 张未提交截图已另行保全并逐文件核对 SHA-256，位置见当前事项的清理记录。
