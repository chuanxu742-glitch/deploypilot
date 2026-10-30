# 界面约定

本页承接已有界面要求，供后续维护使用，不增加新设计决定。当前行为和检查以 README、`web/src` 和 `web/tests` 为定位入口。

## 布局与交互

- 桌面终端在左、任务在右，分隔线可拖动；窄屏切换页签时保留面板及连接。
- 运行、停止中、清理完成由后台状态确认；收到结束总结不能提前放开再次执行。
- 任务记录可折叠、上翻时暂停自动跟随，文本和事件数量有界且截短有提示。
- 设置、历史切换和网络恢复保留清晰反馈；草稿、输入法选词、Enter/Shift+Enter 和终端复制按现有交互测试保护。

## 样式

- 应用使用 Radix 色阶，外部命令输出自身的颜色不受应用配色约束。提供日间、夜间和暖色外观，终端保持深色。
- 应用字号采用 14、24、48px；边框使用 0.5px 变量。
- 图标使用 Lucide SVG；界面文案简洁，不使用装饰性的英文、字符图标或推广标语。
- 无应用渐变、阴影、动画和过渡。保留必要的圆角、焦点、选中、错误和运行状态提示。

静态检查在 `web/tests/ui-style.test.ts`；桌面/手机真实渲染检查在 `web/tests/workspace.browser.cjs`；任务交互在 `task-interactions.browser.cjs`，真实后台联调在 `run-real-backend.cjs`。

## 来源

原始交互要求见[固定版本 requirements.md](https://github.com/chuanxu742-glitch/deploypilot/blob/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/ui-task-experience/requirements.md)。样式约束与后续 14/24/48 字号的批准记录分别保留在[早期修订记录](https://github.com/chuanxu742-glitch/deploypilot/blob/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/ui-task-experience/ui-guidelines-verification.md)和[最新样式改版记录](https://github.com/chuanxu742-glitch/deploypilot/blob/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/ui-task-experience/restyle-verification.md)。旧记录里的 12/14/18 字号不再代表当前约定。
