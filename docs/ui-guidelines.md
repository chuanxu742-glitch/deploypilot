# 界面约定

本页承接已有界面要求，供后续维护使用，不增加新设计决定。当前行为和检查以 README、`web/src` 和 `web/tests` 为定位入口。

## 布局与交互

- 使用可收起的会话导航、当前会话状态栏与底部输入区；侧栏显示本地缩略标题和状态，完整任务保留在对话记录中。桌面可在终端分栏与「专注对话」间切换，点击「显示终端分栏」恢复；偏好在当前浏览器地址下保存。专注模式保留终端挂载、连接与输出，分栏仍可拖动。窄屏保留原任务/终端页签，会话导航为抽屉；抽屉隔离背景焦点，关闭后恢复焦点，选历史或新建后显示任务页。
- 运行、停止中、清理完成由后台状态确认；收到结束总结不能提前放开再次执行。
- 运行时允许只读浏览其他历史，不被活动快照拉回，过程和草稿按会话隔离；提供「返回正在执行的会话」。同一客户端仍串行执行一个任务，浏览历史不占用新的执行槽。
- 旧会话按后端 `canContinue` 恢复能力继续，字段缺失时兼容旧 `continuable` 提示；无法恢复时明确说明并禁发送，不把旧问题下的回答静默发成新任务。已保存对话每份最多 512 KiB；旧记录确无上下文时明确提示摘要恢复，损坏或已保存文件缺失时拒绝。恢复会话不恢复机器进程、Shell 临时状态或旧验证结论。
- 只有选中实际活动会话且仍在执行时才能点击「发送补充」，等待当前执行步骤（完整模型工具组）结束后应用，不中断当前命令。区分 `queued`（已接收待应用）、`applied`（已加入模型上下文）、`deferred`（未应用，须显式继续）；停止清理时禁发送，不自动重开任务或重发命令。补充状态区有界滚动，不能撑满输入区。
- 任务记录可折叠；对话和命令卡内部输出各自支持上翻暂停跟随及明确恢复入口，文本和事件数量有界，截短有提示。助手答复和结束说明使用安全 Markdown，支持标题、列表、表格、链接与代码复制；原始 HTML 不执行、危险协议不生效，外部图片只提供显式打开链接、不自动请求。工具日志保持原始文本。
- 设置、历史切换和网络恢复保留清晰反馈；草稿、输入法选词、Enter/Shift+Enter 和终端复制按现有交互测试保护。
- 未运行时 Enter 发送、Shift+Enter 换行，IME 选词不发送；运行中 Enter 只换行，补充必须点击发送。新任务及补充断线后按请求编号核对事件/快照，不能确认时保留可恢复文本与新草稿，不自动重发；已接收但未应用的补充留在原会话等待显式继续。

## 样式

- 应用使用 Radix 色阶，外部命令输出自身的颜色不受应用配色约束。提供日间、夜间和暖色外观；终端随主题使用浅色、深色和暖色背景，文字、光标、选区及基础 ANSI 色同步调整，切换时保留连接与输出。
- 应用字号采用 14、24、48px；边框使用 0.5px 变量。
- 操作图标使用 Lucide SVG；品牌使用仓库历史中的蓝色 Logo，旁边是名称 BaiLu DeployPilot：BaiLu 用次要文字色和常规字重，DeployPilot 用主文字色加粗；网页标题、设置页版本和安装脚本使用同一名称。界面文案简洁，不使用装饰性的英文、字符图标或推广标语。
- 网页当前保持无渐变、阴影、动画和过渡，保留必要的圆角、焦点、选中、错误和运行状态提示。终端启动页不显示 Logo，以像素字品牌名 BAILU DEPLOYPILOT 为主体：BAILU 用雾蓝退后，DEPLOYPILOT 用蓝色渐变，名字下方一条细线随扫光画出后变暗；窄窗口时 BAILU 改为字距拉开的小字标签。动效为扫描入场和连接动效；动效与真实阶段绑定，二维码及复制链接不受动画覆盖。配对区在窗口足够宽时把说明（有效期、单设备、保持窗口、Ctrl+C）放在二维码右侧，否则放在链接下方；二维码固定白底黑块，主文字跟随终端前景色，浅色终端主题同样可读；链接完整输出不截断。连接后的事件（重试、重建、超时、关闭）统一为“标记 + 说明 + 时刻”的状态行，原因放在下一行。

静态检查在 `web/tests/ui-style.test.ts`；桌面专注模式和手机真实渲染检查在 `web/tests/workspace.browser.cjs`；既有任务交互在 `task-interactions.browser.cjs`，故障回归在 `agent-interaction-regressions.browser.cjs`（`npm --prefix web run test:agent-interactions`）；运行中浏览、续接和补充在 `agent-live-interactions.browser.cjs`（`node web/tests/agent-live-interactions.browser.cjs`）；安全 Markdown 与复制在 `markdown.browser.cjs`（`npm --prefix web run test:markdown`）。真实后台联调在 `run-real-backend.cjs`，替身浏览器测试不证明真实模型行为。

## 来源

原始交互要求见[固定版本 requirements.md](https://github.com/chuanxu742-glitch/deploypilot/blob/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/ui-task-experience/requirements.md)。样式约束与后续 14/24/48 字号的批准记录分别保留在[早期修订记录](https://github.com/chuanxu742-glitch/deploypilot/blob/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/ui-task-experience/ui-guidelines-verification.md)和[最新样式改版记录](https://github.com/chuanxu742-glitch/deploypilot/blob/9d5114c14a988966f9c68865c91338a42962f8bf/docs/changes/ui-task-experience/restyle-verification.md)。旧记录里的 12/14/18 字号不再代表当前约定。
