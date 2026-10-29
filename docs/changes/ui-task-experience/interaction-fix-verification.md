# 交互与按钮文字修复

2026-10-29，版本 `0.2.0-ui7`。本轮只修改前端及对应说明和测试，未提交、推送或发布。

## 原因与修复

- 夜间主按钮原先使用 blue-12 作为文字色，悬停背景也变为 blue-12。浏览器实际测到两者均为 `rgb(194, 230, 255)`，复现用户截图中的空白按钮；移除夜间前景覆盖，保持前景与背景分离，同时提高禁用文字可读性。
- 输入原先仅监听 Ctrl/Meta+Enter；改为 Enter 发送、Shift+Enter 换行，同时保护 isComposing 和 keyCode 229 输入法选词。保留已有运行中不可提交条件。
- 首次默认夜间，保存的日间/夜间/护眼偏好仍有效；首屏脚本与 React 使用一致默认值。设备移至项目旁，外观改为顶栏设置入口与三项选择，支持 Escape 关闭和焦点返回。
- 字体仍使用 Noto Sans SC，增加 Windows 字体回退。未观察到字体文件加载失败，本轮证实的文字消失原因为颜色冲突。

## 实际检查

| 检查 | 结果 |
|---|---|
| 改前浏览器复现 | 新增悬停文字断言失败；前景与背景相同。日志 `.probe-tmp/ui7-red.log` |
| `npm --prefix web test` | 退出码 0，20/20；`.probe-tmp/ui7-unit.log` |
| `npm --prefix web run test:browser` | 退出码 0；`.probe-tmp/ui7-browser.log`。真实 Chrome 渲染、模拟 API/WS，覆盖 Enter 发送、Shift+Enter 换行、两类输入法事件不发送、三主题按钮普通/悬停/焦点/禁用文字对比不低于 4.5、主题保存、模型选择、停止清理、手机布局与连接保持 |
| 视觉检查 | 已查看 `night-button-hover.png`、`mobile.png`，悬停按钮文字可见，桌面/手机顶栏可用 |
| 原生构建 | `go build -ldflags '-X main.version=0.2.0-ui7' -o ../.probe-tmp/deploypilot-ui-v7.exe ./cmd/deploypilot`，退出码 0 |
| 本机实际入口 | 新进程已启动，本机与隧道 health 版本为 ui7，JS/CSS 内容均与当前构建一致。记录 `.probe-tmp/ui-v7-entry-check.json`；保留旧进程避免中断旧任务 |

本轮未更改后端，未重复 Go 全套检查，未调用真实模型或部署服务。输入法保护验证使用浏览器合成事件，未逐一测试所有系统输入法。原生新进程的模型配置与旧进程独立。一次性配对链接与本机身份只保存在私有日志及对话，不写入公开说明。

注释检查覆盖本轮改动，补充 keyCode 229 保护的理由，保留已有连接生命周期注释。
