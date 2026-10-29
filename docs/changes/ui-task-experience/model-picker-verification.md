状态与批准记录见本目录 README。

# 模型列表与界面修订验证

来源：主人截图显示“已获取 45 个模型”，但候选框只显示 bailu-auto；同时要求参考 Gemini 设计风格、不要 emoji。此前的 Radix 色阶、字号、细边框和精简文案约束继续适用。

## 原因与改动

本地 Settings 使用输入框绑定 datalist，当前值为 bailu-auto。[HTML 标准](https://html.spec.whatwg.org/multipage/input.html#the-list-attribute)允许浏览器根据当前输入过滤建议；截图现象与这条机制一致。没有取得用户真实接口响应的逐项记录，不声称另行检查过真实 45 个模型的全部 ID。

新增 ModelPicker，搜索文本与当前模型值分开：展开时显示全部模型，输入时再搜索。菜单使用页面 Radix 色阶，保留任意模型名输入，支持点击、滚动和键盘操作。获取完成后将焦点移至模型输入，便于直接选择。

补测发现菜单展开时，输入框失焦会收起列表并使弹窗重新居中，保存按钮在鼠标松开前上移 27.5px，保存请求数为 0。改为点击完成后处理外部关闭，避免按钮点击落空；同一场景现在一次点击发出一次保存请求。

视觉参考 [Google Design 对 Gemini 的说明](https://design.google/library/gemini-ai-visual-design)，结合部署场景调整留白、终端/助手比例、居中输入和轻量快捷任务。没有复制品牌标志、渐变或动效。原应用中的字符图标改为 SVG 功能图标，增加禁止 emoji/字符图标的检查。

## 证据表

| 结论 | 实际检查 | 结果与局限 |
|---|---|---|
| 状态和样式约束通过 | `npm --prefix web test` | 退出码 0，12 项通过、0 失败，包含无 emoji、Radix 颜色来源、字号和边框约束。 |
| 模型选择及其他交互通过 | `npm --prefix web run test:browser` | 退出码 0；Chrome 加载生产页面，模拟 45 项模型响应。检查全部候选、最后一项点击、重新展开、Home/Enter、大小写不敏感搜索、空结果与 Escape、保存后重开、手机选择，以及原有任务停止/重启/断线和日志交互。原始输出 `.probe-tmp/model-picker-browser-check.txt`。 |
| 列表展开时首次保存通过 | 同一浏览器测试保持菜单展开后点击保存，检查请求计数 | 通过，一次点击发出一次保存请求；修复前同一操作发生超时，记录到按钮上移 27.5px 且无请求。 |
| 前端构建通过 | 浏览器测试包含 `npm run build` | 退出码 0，44 个模块构建完成。 |
| 本机二进制构建通过 | `cd client; go build -ldflags '-X main.version=0.2.0-ui3' -o ../.probe-tmp/deploypilot-ui-v3.exe ./cmd/deploypilot` | 退出码 0；后端任务逻辑本轮未修改，不重跑无关 Go 全套测试。 |
| 新版实际入口可用 | 启动新会话并检查真实隧道 `/api/health`、HTML、CSS 和 JS | 退出码 0，版本 `0.2.0-ui3`；远端 CSS、JS 与本地构建逐字一致；未消耗配对令牌。原会话保留，私有配对链接只在对话中交付。 |

浏览器检查覆盖 320、390、820、1024、1440、1920px 宽度；可见应用文字仍为 12/14/18px，文字与背景来自 Radix 色阶，面板标题和图标中心误差不超过 1px。人工查看 desktop.png、mobile.png、model-list.png、model-mobile.png 与 settings.png；外观是否符合审美仍由主人判断。

本轮使用模拟模型端点，没有发送真实付费模型任务。模型列表交互测试保留在 `web/tests/workspace.browser.cjs`，防止再次把“获取成功”的提示当成“所有模型可选”的证据。没有创建提交、推送或公开发布。
