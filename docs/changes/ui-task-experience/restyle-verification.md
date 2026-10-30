状态与批准记录见本目录 README。

## 范围

2026-10-30 界面样式改版。主人先要求讨论，随后回复“那你来负责修改前端ui样式吧 我完全放心你的审美”，看过改前改后截图后回复“没有意见”。沿用 ui-guidelines-verification.md 的约束：只用 Radix 色阶，字号 14/24/48，边框 0.5px，无阴影、渐变和动效；本轮没有放宽。

## 改了什么

- `index.css` 整体重写为按组件分段，合并此前层层追加的覆盖规则；删除护眼主题中重复的 `--gray-2`。
- 页面底色 gray-2，终端与任务为 12px 圆角卡片，中间 12px 拖拽缝隙（`App.tsx` 的分隔宽度常量由 8 改为 12）。
- 两个面板头部统一为 48px 单行、14px 标题。任务历史改为“历史”按钮外观，底层仍是原生 `<select>`，可访问名称“任务历史”不变。
- 运行状态、计时和“停止”移到输入框上方一行。
- 操作员消息靠右、带底色；工具调用为带边框卡片，命令与输出同一代码块，输出不自动折行、可横向滚动；折叠时标题行显示命令。
- 主按钮改为 gray-12 底、gray-1 字；蓝色只用于焦点、运行中和选中状态。空状态标题由 48px 降为 24px。
- 配对页步骤编号不再重复显示；`.composer-context` 补充消息提示补上灰色说明样式。
- `Icon.tsx` 新增 `history` 图标。

## 证据

| 结论 | 验证方式 | 结果与局限 |
|---|---|---|
| 单元与样式约束通过 | `npm --prefix web test`（Node 22.15 需 `NODE_OPTIONS=--experimental-strip-types`） | 33 通过、0 失败。 |
| 既有桌面/手机流程通过 | `node web/tests/workspace.browser.cjs` | 通过；含字号、配色、对比度、图标对齐和各尺寸无横向溢出检查。本目录截图由此更新。模拟 API/WS。 |
| 任务交互回归通过 | `node web/tests/task-interactions.browser.cjs` | 18/18。首次运行最后一项因预览服务器连接被拒（`ERR_CONNECTION_REFUSED`）失败，重跑通过。 |
| 认证恢复与真实后台联调通过 | `node web/tests/auth-recovery.browser.cjs`、`node web/tests/run-real-backend.cjs` | 5/5；真实 Go HTTP/WS 与 Chrome 联调 `TestFrontendBrowserIntegration` 通过，模型与执行器为本地替身。 |
| 分批提交的中间状态可用 | 在 Git 忽略的临时工作树中依次组装“续聊”“逻辑修复”“样式”三个状态，分别构建和测试 | 三个状态的构建与单测均通过（28、33、33）；逻辑修复状态另跑浏览器三套、真实后台、`go test ./...`、`go vet ./...`，均通过。 |
| 改前改后对照 | 模拟数据截图脚本，夜间/日间/护眼/设置/手机 | 主人看过后无意见。截图曾在 Git 忽略的 `.probe-tmp/ui-review-20261030/`，该临时目录随后被清空，现已不在；本目录的 PNG 是改版后的状态。 |
| 运行中的安装版已包含本次样式 | 对比 `.probe-tmp/frontend-fixes/final-source-after.json` 与当前源码 SHA-256 | `index.css`、`Agent.tsx`、`App.tsx`、`Icon.tsx` 哈希一致；安装版 `v0.4.1-frontend-fixes` 由前端逻辑修复会话构建。 |

## 决定与缺口

- 日间模式下终端保持深色。
- 历史下拉展开后的列表仍是系统原生样式，换取可访问性与现有测试不变。
- 未用真实模型和真实部署输出检查长日志、宽表格和报错堆栈的显示。
