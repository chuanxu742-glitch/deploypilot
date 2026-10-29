# 验证

主机：Windows 11，`go1.26.0 windows/amd64`，Node `v22.15.0`，`CGO_ENABLED=0`。

| 范围 | 命令 | 结果 |
|---|---|---|
| 后端静态检查 | `go vet ./...`；另以 `GOOS=linux go vet ./internal/agent/` 检查 Linux 构建 | 退出码 0 |
| 后端全量测试 | `go test -count=1 ./...` | 8 个有测试的包全部通过 |
| 前端类型检查 | `npx tsc --noEmit` | 退出码 0 |
| 前端单元测试 | `node --experimental-strip-types --test tests/*.ts` | 26 项通过 |
| 前端浏览器回归 | `node tests/workspace.browser.cjs`（Playwright + Chrome，模拟 API/WS） | 两组全部 PASS，无页面错误 |
| 前端生产构建 | `npm run build` | 退出码 0 |

## 新增测试

- `internal/llm/resilience_test.go`：断线归类为中断；`length`/坏 JSON/有 `[DONE]` 但无 `finish_reason` 不算中断；`stop`+工具调用被接受；`Retry-After` 生效；上下文超长识别（OpenAI、vLLM、413 识别，401 和其他 400 不识别）。
- `internal/agent/loop_test.go`：断线后重试本轮且不执行被丢弃轮次的工具；持续断线最终失败且请求次数为 1+2；上下文超长后重试成功；普通 400 不重试；续跑请求包含原任务、此前命令和追加指令，且追加指令是最后一条；中途停止后的上下文工具配对合法；追加指令在压缩后保留；环境探测字段齐全、无 shell 报错、输出为 UTF-8、并行完成。
- `internal/agent/fileops_test.go`：`edit_file` 通过符号链接修改真实文件且链接保留（本机支持符号链接，测试实际执行未跳过）；不留临时文件；`read_file` 拒绝二进制；中文分页不切断字符且能拼回原文。
- `internal/agent/blacklist_test.go`：新增 Windows、`/dev/xvd*`、引号写法的拦截用例和常见 Windows 清理命令的放行用例。
- `internal/server/continue_test.go`：WebSocket 端到端：第一次运行在步数上限结束，快照给出 `continuable`；用 `continueFrom` 继续后成功结束，第二次请求包含原任务、此前命令和追加指令，新记录带 `continuedFrom`；未知任务 ID 被拒绝。
- `cmd/deploypilot/main_test.go`：`DP_MAX_STEPS` 默认值、合法值与非法值。
- `web/tests/hardening.test.ts`：`defaultContinue` 的默认勾选逻辑。

## 环境探测实测（本机）

```
OS: Microsoft Windows 11 家庭中文版 10.0.26300 64-bit
User: CHUAN\32536; administrator: False
CPU/Mem: 16 logical CPUs; 15.7 GB RAM
Disk: C: free 12.9 GB
GPU: NVIDIA GeForce RTX 3050 Laptop GPU, 4096 MiB, 581.95
Python: Python 3.11.9
Docker: Docker version 29.8.0, build 88096ef
Network: 200
```

强制 UTF-8 之前，系统名里的中文显示为乱码；之后正常。

## 未覆盖

- Linux 上的探测命令（bash 版）和 `preserveOwner` 的 chown 只经过 Linux 交叉编译与代码审查，没有在 Linux 真机运行。
- 没有用真实的白鹿模型做端到端联调；所有模型交互都用本地模拟服务器验证。
