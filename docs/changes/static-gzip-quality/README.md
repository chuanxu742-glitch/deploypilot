# 静态资源忽略小数形式零质量 gzip 的修复

## 版本和交付分支

问题位于 `client/internal/server/static_assets.go`。已发布 v0.4.10 对应 `5f9899a55f4f66fe9bab979ab0b624f55a06f319`；目标是现有发布分支 `codex/tunnel-diagnostics`，当前基线 `bb9524ea4802cdb958abf3a130b0676fdc8d8b34` 只比发布标签多一个文档提交，运行代码相同。

当前 main `9d5114c14a988966f9c68865c91338a42962f8bf` 尚无此压缩功能。它来自未合并 PR #2（head `c6c8c03576d7a1ab4908b96949886ee7ba1aadaf`），其中该文件与发布版一致。因此本修复只针对包含此功能的发布分支，不将整个发布分支混入 main。若以后要把修复纳入 main，应先纳入 PR #2 的功能或等价代码，再移植这个修复。没有合并、改动现有分支或部署。

## 普通 HTTP 复现

客户端请求一个可压缩的 6000 字节 JavaScript 资源，并发送 `Accept-Encoding: gzip;q=0.0, identity;q=1`。原实现只对字符串 `q=0` 特判，实际响应为 HTTP 200、`Content-Encoding: gzip`、86 字节压缩体，而客户端已经排除 gzip。

`q=0.00`、`q=0.000` 同样错误；GET/HEAD 共6项断言失败。`q=0`、正质量值、常见gzip请求、Range响应均为正常对照。

[RFC 9110 §12.4.2](https://www.rfc-editor.org/rfc/rfc9110.html#section-12.4.2) 允许零值带小数部分；[§12.5.3](https://www.rfc-editor.org/rfc/rfc9110.html#section-12.5.3) 规定质量0表示不接受该编码。本修复按数值解析q；缺省保持允许gzip，正值保持压缩，零值走原有未压缩响应。

## 真实执行范围

从固定发布版取回55份相关生产源文件/模块文件并校验 Git blob SHA。最终复现执行真实 `New → Handler → handleSPA → serveAsset` 路由，使用 `httptest.NewRequest/ResponseRecorder` 和内存 `fstest.MapFS`。未调用 `Start`，未监听端口，未调用模型、终端、隧道或真实部署，未访问真实项目或账户。

最初两个字段的隔离接收器试验也复现了问题，但这里交付的日志、测试和结论来自完整生产 Server 类型及路由，不交付该接收器替身。普通静态资源请求无需配对，`New` 的 HistoryDir 为空时不会读取历史目录。

## 回归

```sh
cd client
go test -count=1 -v -run '^(TestAcceptEncodingZeroQuality|TestPositiveQualityAndRangeControls|TestStaticAssetsAreCachedAndCompressed)$' ./internal/server
go test -race -count=5 -run '^(TestAcceptEncodingZeroQuality|TestPositiveQualityAndRangeControls|TestStaticAssetsAreCachedAndCompressed)$' ./internal/server
go vet ./internal/server
```

Go 1.27.3。相同测试在原版有6个失败子用例，修复后3个顶层测试、14个子用例全部通过，含原有缓存/压缩测试。race重复5轮通过（15个顶层通过事件、70个子用例通过事件，不算独立测试数）；vet退出0、无诊断；gofmt通过。

- [修复前输出摘要](before.txt)、[修复后输出摘要](after.txt)：从真实完整测试日志提取请求/响应字段。
- [原始失败日志](baseline.log)、[修复日志](fixed.log)、[race原始JSONL](race.jsonl)、[vet结果](vet.txt)。
- 实际GitHub日志页截图：[修复前](before-github.jpg)、[修复后](after-github.jpg)。拍摄于提交 `3627d059900f06390876c9aadc419f71dd0bcbc1`，均为未经编辑的原生1197×807 JPEG，完整显示11行日志；不是产品UI或真实网络抓包。

仅修复显式gzip质量值处理，不声称实现全部内容协商策略。没有跑全Go库、Live/MachineInfo、应用启动、浏览器E2E或实际渲染。日志截图仅证明真实GitHub日志页面，不是产品UI验收。
