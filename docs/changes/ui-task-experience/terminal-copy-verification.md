# 终端快捷键复制

2026-10-29，ui10，仅修改前端和验证材料，未提交推送。

原因：当前 xterm 默认键盘处理将 Ctrl+C 转为 ETX 中断字符并取消浏览器默认操作；应用没有优先处理终端选区。Agent 只读时中断字符被拦截，但复制仍然不发生。

修复：两个内部终端屏幕都安装复制快捷键处理。有选区时 Ctrl+C、Ctrl+Shift+C、Cmd+C 使用原生 copy 事件，由 xterm 提供选区内容；必要时回退 Clipboard API。复制优先于终端输入，短暂显示“已复制”或“复制失败”，不添加常驻说明。无选区 Ctrl+C 继续由终端处理，复制专用快捷键不产生中断。

验证：

- 改前新增浏览器测试失败：真实鼠标选行后 Ctrl+C 没有将该行复制到剪贴板。日志 `.probe-tmp/ui10-red.log`。
- `npm --prefix web test`：退出码 0，22/22；`.probe-tmp/ui10-unit.log`。
- `npm --prefix web run test:browser`：退出码 0；`.probe-tmp/ui10-browser.log`。真实 Chrome 和剪贴板，模拟 API/WS。覆盖人工态和 Agent 只读态的三种复制组合键；确认复制不发送数据、不切换终端，普通 Ctrl+C 在无选区时发送 ETX，Ctrl+Shift+C 无选区不发送 ETX。并回归既有布局、模型、停止和终端联动检查。
- Cmd 修饰键在 Windows Chrome 自动化环境中模拟，未在 macOS 实机测试。
- 本轮未改后台，未重复 Go 全套测试。注释补充复制优先、中断保留以及原生 copy 的原因。
- ui10 原生构建退出码 0，新版进程已启动；首次隧道资源检查出现一次传输 EOF，重试后本机与隧道的 health/JS/CSS 均与当前 ui10 构建一致，检查退出码 0。记录 `.probe-tmp/ui-v10-entry-check.json`。配对令牌未消耗，原会话未中断。
