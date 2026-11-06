# 前端长文本 UTF-16 边界修正

基线：main `9d5114c14a988966f9c68865c91338a42962f8bf`，Node 24.22.1，2026-11-06 UTC。

`mergeEvent` 在累计文本超过 32768 个 UTF-16 代码单元时，从固定下标截取末尾。这一下标可以落在合法 emoji 的两个代理单元之间，留下孤立的低代理单元。例如三个各自合法的分片、最大分片 32755 字节，合计 32769 个 UTF-16 单元，输出会在截断提示后保留 `0xde80`。`normalizeEvents` 也会复用此路径。

这是前端字符串截断问题，独立于后端 UTF-8 分片的 Issue #12 / PR #13。即使每个输入分片都是完整合法 Unicode，仍可触发。修正只在截断位置正好位于合法高/低代理对之间时向后移一个单元，保留整个字符边界，仍不超过原有上限。已有非法字符串的修复不在本次范围内。

## 实际执行结果

- 相同 18 项 agent-state 测试：main 13 通过、5 失败；修复后 18/18。
- 未合并 PR #2 的 `agent-state.ts`（head `c6c8c03576d7a1ab4908b96949886ee7ba1aadaf`）也有相同 5 个失败。本修复独立基于 main，没有叠加该分支。
- 全部前端 Node 测试 40/40 通过；TypeScript/Vite 构建通过；两处变更文件 Prettier 通过。
- 对照覆盖 ASCII、BMP 中文、完整 emoji 边界、恰好上限、连续追加、恢复记录。

复现：

```sh
node docs/changes/frontend-unicode-clipping/reproduce.ts
cd web
node --test tests/agent-state.test.ts
npm test
npm run build
```

将新回归测试及复现脚本复制到 main 的独立工作区，不替换生产文件，即可观察原始失败。

[before.txt](before.txt) 和 [after.txt](after.txt) 为实际生产 TypeScript 函数运行输出；详细日志见 [基线](unicode-baseline.log)、[PR #2 基线](unicode-pr2-baseline.log)、[修复](unicode-fixed.log)、[全前端测试](web-tests.log)、[构建](web-build.log)、[格式](format-check.log)。

实际 GitHub 日志页面截图：[修复前](before-github.jpg)、[修复后](after-github.jpg)，拍摄于提交 `ad5d4b9a80ee6241c166381e5395794425656337`，原生未编辑 JPEG，1197×807。不是产品 UI 截图。

浏览器验证曾尝试，但 Chromium 在页面加载前因 socket 权限及只读 crash-report 配置失败。未重试、未提升权限、未更改安全设置，没有取得 DOM 或产品 UI 截图。此处不宣称浏览器 E2E、真实后端、模型调用或部署通过；截图仅为实际 GitHub 测试日志页面。未运行 Go 后端全库测试。
