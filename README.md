# BaiLu DeployPilot 远程部署助手

## 简介

BaiLu DeployPilot（下文简称 DeployPilot）是一个运行在客户 Linux 机器上的单文件客户端。客户执行一行命令后，程序通过 Cloudflare 快速隧道暴露一个本地 Web 控制台，操作员扫码即可获得：

- 远程终端（xterm.js）
- AI 部署助手（白鹿大模型，OpenAI 兼容接口），可自动执行命令、读写文件完成部署

> ⚠️ **警告**：DeployPilot 会把客户机器的完整控制权交给操作员和 AI。只能在客户明确同意的前提下运行。终端与 AI 均以启动程序的用户身份执行，用 root 启动即拥有 root 权限。

## 工作流程

```mermaid
flowchart LR
    A[客户执行 install.sh] --> B[下载并校验 deploypilot]
    B --> C[启动本地 Web 服务<br/>127.0.0.1:随机端口]
    C --> D[下载并校验 cloudflared]
    D --> E[启动 quick tunnel]
    E --> F[终端打印二维码与链接]
    F --> G[操作员扫码配对]
    G --> H[远程终端 + AI 部署助手]
```

1. 显示品牌名 BaiLu DeployPilot 与版本；支持彩色输出的交互终端播放约 1 秒的扫光入场（期间服务照常启动，按 Ctrl+C 直接跳到完成画面），之后以呼吸点和流光文字显示实际连接阶段。
2. 在 `127.0.0.1` 的随机端口启动 Web 服务。
3. 下载固定版本 cloudflared `2026.10.5` 到 `~/.deploypilot/bin`，用内置 sha256 校验；依次尝试 `github.com`、`gh-proxy.com`、`ghfast.top`。
4. 启动 quick tunnel，打印二维码和链接 `https://xxx.trycloudflare.com/#t=<token>`。
5. 按 Ctrl+C 结束会话并关闭隧道。

## 快速开始

### 客户侧

支持 Linux amd64 / arm64：

```sh
curl -fsSL https://github.com/chuanxu742-glitch/deploypilot/releases/latest/download/install.sh | sh
```

脚本会识别架构，从 GitHub Releases（`chuanxu742-glitch/deploypilot`）下载 `deploypilot-linux-<arch>` 和 `checksums.txt`，校验 sha256 后直接运行。

国内网络可加镜像前缀：

```sh
curl -fsSL https://gh-proxy.com/https://github.com/chuanxu742-glitch/deploypilot/releases/latest/download/install.sh | DP_MIRROR=https://gh-proxy.com/ sh
```

运行后把终端里的二维码或链接发给操作员。

#### 客户是 Windows

在 PowerShell 里执行：

```powershell
irm https://github.com/chuanxu742-glitch/deploypilot/releases/latest/download/install.ps1 | iex
```

国内网络较慢时：

```powershell
$env:DP_MIRROR='https://gh-proxy.com/'; irm https://gh-proxy.com/https://github.com/chuanxu742-glitch/deploypilot/releases/latest/download/install.ps1 | iex
```

Windows 下控制的是这台 Windows 机器本身，网页终端用的是 PowerShell（通过 ConPTY）。目前提供 amd64 版，arm64 机器会以模拟方式运行 amd64 版。

Windows 终端将完整当前目录放在上一行，下一行使用短 `> ` 提示符，保留行编辑、历史和补全，避免长目录在网页缩窄后让 PSReadLine 使用越界的输入起点。

### 操作员侧

1. 扫码或打开链接，自动完成配对。
2. 桌面左侧为远程终端，右侧为部署任务，也可点击「专注对话」扩大对话区；手机通过顶部页签切换。
3. 在「模型设置」中填写 API Key（如 `sk-xxxx`）和模型，点「获取列表」可查询可用模型。设置只保存在内存中。
4. 用自然语言描述部署任务，AI 会全自动执行，可用工具：`run_command`、`read_file`、`write_file`、`edit_file`、`append_file`、`wait_for`、`finish`。默认不限制步数（与 pi、oh-my-pi 一致）：普通问答、读取文件或执行检查后的非空自然回复，都可以正常结束本轮，不因缺少 `finish` 被判失败，也不自动标记部署成功。要明确报告已验证成功，仍须用 `finish(success=true)` 引用实际验证记录；真实失败可用 `finish(success=false)`，需要补充信息则显示中性的「等待补充信息」，你补充后可继续。连续空回复只提醒一次，再次为空才报告未取得有效答复。需要上限时设置 `DP_MAX_STEPS`。工具串行执行，每条命令默认超时 600 秒，最长 3600 秒。
5. 从会话栏选择已结束的任务，后台确认可恢复时，输入消息可继续同一会话，包括较早任务和程序重启后的记录。无法恢复时会明确提示并禁止发送，不把回答静默变成新任务；点击「新建会话」开始独立会话。恢复对话后仍须重新检查机器和部署结果。

### 工作台操作

- 桌面默认左侧终端约占 60%，可拖动分隔线或聚焦后用方向键调整。顶栏「专注对话」隐藏终端分栏、扩大对话区，「显示终端分栏」恢复原分栏；偏好保存在当前浏览器地址下，刷新后保留。终端组件保持挂载，切换不会重建连接、清空输出或重启 AI 任务；手机仍使用顶部页签。
- 左侧终端与 Agent 共用同一个持续运行的 Shell。切换目录和环境变量对后续命令与人工操作都生效；Agent 运行及清理期间暂停人工输入，结束后在原终端接续操作。任务面板显示命令退出码，终端展示实际输出。
- 选中终端文字后按 `Ctrl+C`、`Ctrl+Shift+C` 或 macOS 的 `⌘+C` 复制，Agent 运行期间也可复制。无选中文字时，`Ctrl+C` 保留终端中断行为；Agent 只读期间不会发送中断。
- 顶栏右侧的外观设置提供日间、夜间和护眼模式，首次默认夜间；手动选择保存在当前浏览器地址下。终端随主题切换浅色、深色和暖色背景，文字、光标、选区及基础 ANSI 色同步调整，保留连接和已有输出。护眼为暖色外观，不代表健康效果。点击项目旁的设备名称可查看系统、运行用户和启动目录。
- 点击示例填入任务，或自行输入；未运行时 `Enter` 执行、`Shift+Enter` 换行，中文输入法选词不会发送。运行中选中实际活动会话后，可点击「发送补充」；此时 Enter 只编辑换行，不发送。补充在当前执行步骤（完整的一组模型工具调用）结束后、下一次模型请求前应用，不会立即打断命令。停止清理期间禁止发送；要开始独立任务，等待清理完成后点「新建会话」。
- 补充显示接收与应用的不同阶段：`queued` 表示后台已保存、等待应用，`applied` 表示已加入模型上下文；停止或错误结束前尚未应用的补充显示为 `deferred`，保留在原会话，须手动继续后处理，不会自动重开任务。后台按任务编号和请求编号去重；同一编号的重复接收回执不代表重复应用。单条补充最多 16 KiB，待应用队列最多 16 条、合计 64 KiB，超过时明确拒绝并保留可恢复文本。
- 任务显示连接中、执行中、停止中和已结束。点击停止后等待清理确认，才可启动下一次任务；单次工具错误会展示在记录中，不代表任务已经结束。
- 运行记录保留公开的助手进展说明，显示收集环境、等待模型和执行工具的真实活动。运行中补充提示保持简短，不重复展开完整任务；当前命令卡显示本次调用的已运行时间和已知时限。命令详情显示设备、启动目录、退出码、易读耗时与独立的完整日志路径；已取消、超时和执行失败分别显示，执行中、取消及出错默认展开。上翻日志时暂停自动跟随，点击“返回最新记录”继续跟随。浏览器保留最近 300 条记录，并限制每条文本长度，截断时会显示提示。
- 助手答复和结束说明支持 Markdown 标题、列表、表格、代码块与链接。代码块可复制，失败时显示手动复制提示。原始 HTML 不执行，不接受危险链接协议；图片仅显示「查看图片」链接，打开前不自动请求外部图片。工具日志仍按原始文本展示。
- 点击输入框左下方的模型名称（未配置时显示“配置模型”）打开设置；已有 API Key 时留空保留。输入 API Key 后可直接获取模型列表，密钥会先保存在当前进程内存。任务运行时设置按钮暂不可用，运行任务持有启动时的模型配置。
- 获取模型列表后，展开选择框可查看全部模型，输入文字可搜索；支持滚动、方向键和 Enter 选择，也可手动输入模型名。
- 终端断开后点击“重新连接”即可回到原 Shell 并回放近期输出。AI 连接自动重连并恢复任务状态，刷新页面不会取消任务，也不会重新发送任务。新任务或运行中补充提交后断线，会用请求编号核对事件与快照；无法确认时保留可恢复文本，不自动重发，也不覆盖断线后新写的草稿。停止任务须使用“停止”；认证过期、主动撤销授权或程序退出仍会取消任务。
- 运行中也可只读浏览其他历史，活动任务继续执行，过程和草稿不会串到所选历史；点击「返回正在执行的会话」回到活动任务。输入区说明草稿归属，其他会话的草稿不能发送给后台活动任务。同一网页内的未发送草稿按会话暂存，切换返回可继续编辑；这些草稿只保存在网页内存中。
- 任务记录保存在本机 `~/.deploypilot/history/<工作目录标识>/`，不因新建任务删除旧会话。每个任务的过程内容受 512 条事件和约 512 KiB 预算限制，未应用补充优先保留；终端保留最近 2 MiB 输出。首次升级会从最近 100 份旧版执行日志恢复已记录事实，不改动原文件。
- 对话另以 `conversation-<任务编号>.json` 保存于同一私有历史目录，每份最多 512 KiB，超出时压缩。后台通过 `canContinue`（该会话当前是否可恢复）告知界面能否继续。确无已保存上下文的旧记录可从有界事件恢复摘要，并明确显示「历史摘要恢复，需要重新检查」；已保存文件损坏、身份不符、缺失或工具调用不完整时拒绝恢复，不静默降级。恢复后旧验证不能用于本轮成功结论，也不会重建原 Shell、机器进程或临时环境。
- 同一客户端只允许一个 AI 任务，跨标签页和重连也受限制；旧任务清理完成后才允许再次执行。Windows 命令取消或超时会清理本次调用登记的新建后代进程，保护调用前已有的后台服务；正常完成时刻意启动的后台服务可以继续运行。登记通过进程快照轮询完成，可能漏掉两次扫描之间已退出的中间父进程；由外部服务代建的进程不在追踪范围，这不是沙箱。
- 长任务会自动压缩上下文：先截短较早的工具输出，保留首尾，再把完整执行轮次整理为历史摘要；始终保留系统约束和原始任务。压缩会显示提示，不额外调用模型。较早细节可能被省略，需要时应重新读取文件或检查机器。
- 工具参数缺失、类型错误、空命令/路径等会被拒绝并显示错误；模型流中断、输出长度截断或调用数据不完整时，不执行该响应的工具，也不报告任务成功。
- 接口返回 HTTP 429/5xx，或响应流中明确返回 `upstream_busy`、数字或数字字符串形式的 429/500–599 错误码时，按既有退避最多尝试 8 次，网页显示等待重试，可随时停止。其他 4xx 和未知流内错误不因此重试。繁忙重试只重发当前模型请求，丢弃错误响应中未执行的工具，不重放之前已完成的命令；已经显示的部分文字可能重复。持续繁忙达到上限后提示稍后继续当前任务或更换模型。
- 同一工具、参数和结果连续出现 3 次时提醒模型换方法；第 4 次仍相同则暂停任务，保留上下文供继续。正常有新输出的等待不按重复处理。
- 命令完整输出写入本机 `~/.deploypilot/logs/command-*.log`，模型收到的有界输出末尾附绝对路径。超时或取消也保留日志；日志写入失败会明确报错。Windows 命令卡在运行期间接收格式化文本输出，完整持久终端日志仍来自实际终端输出，可能含终端控制字符。日志目前不自动清理；已脱离的后台服务后续输出仍需读取服务自己的日志。
- `write_file`、`edit_file` 修改已有文件前在同目录生成唯一 `.bak-*` 备份；备份失败不继续覆盖，新建文件不生成备份。通过 Shell 命令进行的修改和 `append_file` 不在该备份范围内。
- `wait_for` 在程序内部等待 URL 返回 HTTP 2xx/3xx、日志包含指定文本或指定 PID 不再存在，不为每次轮询调用模型。默认等 300 秒、间隔 2 秒，最长 3600 秒，可随任务停止。网页“等待条件”卡片显示等待对象、已等待时间、时限和最近观察，状态变化或约每 10 秒更新；刷新后恢复最新状态，完成后可展开看实际结果。日志首次扫描最近 1 MiB，之后按页接着读；PID 不存在只证明进程已结束，不证明进程执行成功。
- `finish(success=true)` 必须用 `verification_call_ids` 引用本轮真实成功的 `run_command` 或 `wait_for` 调用；最终总结附实际命令/条件与结果。新命令或文件修改使旧证据失效，继续任务后也需重新检查。无证据或引用失败调用时退回给模型继续验证。该检查保证证据来自实际执行，验证内容是否足以证明部署目标仍需判断。
- `finish` 的可选 `outcome` 支持 `completed`、`failed`、`needs_input`：分别要求成功、失败、等待操作员补充；等待时 `success=false`，验证编号为空或省略。兼容旧调用省略 `outcome`，矛盾状态仍会拒绝。

## 配置

| 变量 | 作用 | 默认值 |
| --- | --- | --- |
| `DP_VERSION` | install.sh 安装的版本标签 | `latest` |
| `DP_BASE_URL` | 发布文件的完整基础 URL | GitHub Releases |
| `DP_MIRROR` | 加在 GitHub URL 前的镜像前缀，如 `https://gh-proxy.com/` | 空 |
| `DP_INSTALL_DIR` | 二进制安装目录 | Linux `~/.deploypilot/bin`；Windows `%LOCALAPPDATA%\DeployPilot` |
| `DP_CLOUDFLARED_MIRROR` | cloudflared 下载源，逗号分隔，每项作为 GitHub 路径前缀，如 `https://gh-proxy.com/https://github.com` | 内置列表 |
| `DP_CLOUDFLARED_BIN` | 使用已有的 cloudflared，跳过下载 | 空 |
| `BAILU_API_KEY` | 白鹿 API Key（如 `sk-xxxx`） | 空 |
| `BAILU_MODEL` | 模型名 | `bailu-auto`（已验证支持流式输出和工具调用） |
| `BAILU_BASE_URL` | OpenAI 兼容接口地址 | `https://bailucode.com/openapi/v1` |
| `DP_CONTEXT_BYTES` | 每次模型请求 JSON 的字节预算，含消息和工具定义；允许 16384–2097152 | `262144`（256 KiB） |
| `DP_MAX_STEPS` | 每次运行的模型轮次上限；`0` 表示不限制，设置上限时至少为 5，用完后可在该任务上继续 | `0`（不限制） |
| `NO_COLOR` | 关闭横幅颜色 | 空 |

## 隧道恢复

- 启动最多等待 120 秒；取得临时网址且 cloudflared 确认首次边缘连接后显示二维码，公共 DNS/路由可能还需要短暂传播时间。
- 短暂网络中断由 cloudflared 自行重连。进程退出或启动失败时，客户端按 1、2、4、8 秒退避；连续 5 次失败后退出并显示原因，运行超过 1 分钟重置该计数。
- 进程重建后会换网址，旧会话和旧配对链接立即失效；请使用终端里的新二维码重新配对。中断任务不会自动重放；历史记录、近期终端输出和最后工作目录会恢复。程序重启会新建 Shell，旧进程、临时变量及环境修改不能恢复。
- 任务运行和清理期间保留所属会话，静默下载、编译或等待不会仅因 30 分钟没有输出而取消；结束后重新开始完整的 30 分钟空闲计时。主动撤销授权、隧道重建或程序退出仍会使旧会话失效。空闲超时后，若隧道仍在线，终端会自动打印新的二维码和链接；重新扫码即可接入，无需重启程序。
- 网址可以重复打开，一次性的是二维码/链接里的首次配对令牌。同一浏览器保存的登录 Cookie 仍有效时，新标签页、刷新或关页后重开原链接都能进入，不需要再次消费令牌；换浏览器、使用无痕窗口或 Cookie 丢失/过期后，需要新的配对链接。浏览器会话 Cookie 能否跨浏览器完全退出保留取决于浏览器；隧道重建后必须使用新网址。同一客户端仍只允许一个 AI 任务同时运行。
- 网络恢复后会检查会话是否过期；明确失效时回到配对页，网络故障不会直接当作过期。同一浏览器已有有效会话时，再次打开原始一次性配对链接可以直接进入工作台。
- 下载 cloudflared 与访问模型接口都遵循 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` 等标准代理环境变量。
- WebSocket 每 25 秒发送 ping；75 秒没有 pong 回复就关闭失联连接并进入既有任务清理流程。心跳不延长认证有效期。
- cloudflared 下载从请求开始每 10 秒检查进度；响应头卡住或窗口内不足 512 KiB 时切换下一源，每源最长 5 分钟。下载文件独立隔离，校验成功后才替换缓存。

## 安全说明

- **配对令牌**：32 字节随机数，放在 URL fragment（`#t=`）中，不会发送到服务端或出现在 Cloudflare 日志里；一次性使用，10 分钟过期。
- **会话**：同一时间只允许一个会话；Cookie 为 `HttpOnly` + `Secure` + `SameSite=Strict` 的浏览器会话 Cookie。运行中的任务持有所属会话直至清理完成，之后按空闲 30 分钟失效，包括已建立的 WebSocket；实际输入和任务输出续期，自动 ping/pong 不续期。持有不会复活过期会话或阻止主动撤销。
- **WebSocket** 校验 Origin。
- **退出**：Ctrl+C 吊销会话、关闭服务和隧道。
- **API Key** 只保存在内存中，错误信息里会脱敏。通过 `BAILU_API_KEY` 提供时，读取后会立即从进程环境变量中移除，避免终端或 AI 执行 `env`/`printenv` 读到它。
- **审计日志**：`~/.deploypilot/audit-<时间>.jsonl`。
- **对话记录**：上下文和补充保存在本机私有历史文件中，未加密；它们可能包含任务、文件和命令输出。不保存模型配置中的 API Key 或执行器状态；旧检查结果可作为历史事实保留，恢复后必须重新取得成功验证证据。
- **命令黑名单**：拦截 `rm -rf /`（含加引号的写法）、`mkfs`、`dd` 写块设备（含 `/dev/xvd*`）、`wipefs`、`shred /dev/*`、fork bomb、`shutdown`/`reboot`/`poweroff`、`chmod -R 777 /`、`chown -R ... /`；Windows 上拦截 `Format-Volume`、`Clear-Disk`、`diskpart`、`format C:`、递归删除盘符根目录或 Windows 目录、`Stop-Computer`/`Restart-Computer`、`bcdedit /delete`、`vssadmin delete shadows` 等。黑名单只检查 `run_command` 的命令文本，是兜底，不是沙箱。
- 再次提醒：只在客户同意时运行，权限等同于启动它的用户。

## 开发与构建

维护文档见 [文档入口](docs/README.md)；界面要求见 [界面约定](docs/ui-guidelines.md)。浏览器测试截图写入 `.probe-tmp/ui-screenshots/`，不会生成已跟踪的图片改动。

### Windows

Go 优先使用 `.tools\go`，找不到时用系统 `go`（也可通过 `GO` 环境变量指定）。

```powershell
npm --prefix web install
npm --prefix web run build
npm --prefix web test      # Node.js 22.18+：任务状态与日志边界测试
npm --prefix web run test:browser  # 需要已安装 Chrome；使用模拟 API/WS，不调用真实模型
npm --prefix web run test:integration  # 需要 Chrome 与 Go；真实后台联调，模型与执行器使用本地替身
npm --prefix web run test:agent-interactions  # 需要 Chrome；独立开发服务器上的草稿、停止、手机抽屉和日志滚动回归，模拟 API/WS
npm --prefix web run test:markdown  # 需要 Chrome；安全 Markdown、复制和窄屏内容回归，不调用真实模型
node web/tests/agent-live-interactions.browser.cjs  # 需要 Chrome；运行中浏览历史、按能力续接、补充接收/应用与断线恢复，模拟 API/WS
npm --prefix web run format  # Prettier 统一格式化 web/src 与 web/tests；format:check 只检查
client\build.ps1          # 交叉编译 linux amd64/arm64 到 dist\，并生成 checksums.txt
client\build.ps1 -Local   # 编译本机二进制
client\build.ps1 -Test    # 运行 go test ./...
```

端到端 agent 场景测试使用真实服务端、真实持久终端（Windows 为 ConPTY + PowerShell）和脚本化模型，覆盖部署验证、命令失败与恢复、超时、交互提示、`exit`、大量输出、坏参数、finish 证据、重复保护、断流与超长重试、补充与停止，约 3 分钟，不调用真实模型：

```powershell
$env:DP_AGENT_DEEP='1'; go test ./internal/server -run '^TestAgentDeep$' -v -timeout 20m   # 在 client 目录执行
```

真实模型部署测试让模型在临时目录里用 Python 标准库部署一个 HTTP 服务（不安装软件），结束后由测试独立请求服务核对内容，并清理遗留进程。需要 API Key，会调用真实模型：

```powershell
$env:DP_AGENT_LIVE='1'; $env:BAILU_API_KEY='<key>'; go test ./internal/server -run '^TestAgentLiveDeploy$' -v -timeout 12m
```

真实模型复杂场景测试覆盖排查并修复坏服务、在大日志里找根因、信息不足时提问、拒绝破坏性清理、忽略文件里的注入指令、任务中途改需求、停止后继续，约 6～10 分钟。命令经过测试内的守卫执行器：删除、移动、写文件只允许在场景临时目录内，拒绝系统改动和安装，只能停止测试自己启动的进程；破坏性请求场景完全不执行命令。可用 `DP_AGENT_LIVE_ONLY=<场景名>` 只跑单个场景：

```powershell
$env:DP_AGENT_LIVE='1'; $env:BAILU_API_KEY='<key>'; go test ./internal/server -run '^TestAgentLiveComplex$' -v -timeout 70m
```

真实模型浏览器测试用 Chrome 操作构建后的页面，连接真实 Go 服务、真实终端和真实模型，命令同样经过守卫执行器。覆盖配对、部署中刷新、第二个标签页、同会话续聊、运行中查看历史、停止后继续、运行中补充需求、补充信息、关闭标签页后回来、手机布局，约 8～12 分钟。截图和 `results.json` 写到 `DP_LIVE_BROWSER_OUT`；`DP_LIVE_BROWSER_ONLY=<场景名,...>` 只跑部分场景。先执行 `npm --prefix web run build`：

```powershell
$env:DP_AGENT_LIVE='1'; $env:DP_BROWSER_INTEGRATION='1'; $env:BAILU_API_KEY='<key>'; go test ./internal/server -run '^TestAgentLiveBrowser$' -v -timeout 50m
```

### Linux / CI

```sh
make build
make test
make lint
make e2e-docker
```

### 联调白鹿接口

```powershell
# 先把 API Key 复制到剪贴板
powershell -ExecutionPolicy Bypass -File scripts\live-test.ps1 -FromClipboard              # 自动选第一个模型
powershell -ExecutionPolicy Bypass -File scripts\live-test.ps1 -FromClipboard -Model <名>
```

不加 `-FromClipboard` 时脚本会弹出隐藏输入框，那里不能用 Ctrl+V，要用鼠标右键粘贴。脚本读取的 API Key只在本次进程内有效，不写入磁盘和命令历史。它会依次测试模型列表、流式对话、工具调用，最后用模拟服务器跑一遍完整的 agent 多步流程。Linux / CI 上可以执行 `BAILU_API_KEY=... go test -run Live -v ./internal/llm/ ./internal/agent/`。

### 发布

目前发布由本地脚本 `scripts\publish.ps1` 完成：它用 `client\build.ps1` 交叉编译、生成 `checksums.txt`，再上传二进制、`checksums.txt`、`install.sh`、`install.ps1` 到 GitHub Release，并回读校验线上文件与本地一致。仓库暂未包含 `.github/workflows` 自动发布流水线。

## 目录结构

```
client/
  cmd/deploypilot/     程序入口
  internal/
    banner/            启动画面与连接进度
    server/            本地 HTTP / WebSocket 服务
    auth/              配对令牌与会话
    tunnel/            cloudflared 下载、隧道、二维码
    pty/               伪终端
    llm/               OpenAI 兼容客户端
    agent/             AI 代理、工具、命令黑名单
    webui/             内嵌前端资源
web/src/               前端源码（xterm.js 控制台）
scripts/install.sh     客户侧安装脚本
```

## 已知限制

- Agent 的命令结果保留最多 16 KiB 的开头和末尾，并明确标注中间截断；完整输出另存日志供搜索。上下文回收优先处理旧历史，尽量保留最近完整工具轮次。工具失败会返回错误及结束状态；普通文字回复是中性的本轮答复，不自动算部署成功，也不因缺少 `finish` 失败。`finish(success=true)` 检查实际验证证据，但不代替独立部署验收。
- 模型回复中途断线或响应流连续 180 秒无网络数据时（本轮不会执行任何工具），会自动重新请求本轮，最多 2 次，界面上可能出现重复的文字。正常持续传输的长回复不受该空闲时限影响。供应商返回“上下文超长”时，会收紧上下文预算并重试，最多 2 次。响应流开始前的网络错误、429/5xx 最多尝试 8 次，从 2 秒起指数退避、每次最长 60 秒，按更长的 `Retry-After` 等待（仍受 60 秒上限）；等待进度显示在任务记录中，停止任务会取消等待。
- 开始任务前会探测系统、用户及是否有免密 sudo、CPU/内存、磁盘、GPU、CUDA、Python、Docker、systemd 和网络，作为模型的环境信息；这些探测以独立的只读进程运行，不出现在终端里。
- `edit_file` 会修改符号链接指向的真实文件，并保留原文件的权限和属主；`read_file` 拒绝读取二进制文件，分页时不会切断多字节字符。
- 上下文预算按请求 JSON 的字节计算，不是精确 token 数，也不能自动识别供应商的模型窗口。使用较小窗口的模型时应降低 `DP_CONTEXT_BYTES` 并给回复留出空间；原始任务本身超过预算会明确报错，不会静默删除目标。
- trycloudflare 快速隧道适合临时接入，官方不提供在线时间保证，重建会改变网址；固定域名和生产接入应使用正式 Cloudflare Tunnel。不同地区和运营商的实际可达性需要实测。
- 支持 Linux amd64 / arm64 和 Windows 10 及以上（amd64）。Windows 7/8/8.1 无法运行：构建所用的 Go 1.21 起不再支持这些系统，安装脚本会直接提示。
- 启动时会检测终端：Windows 经典控制台会主动开启颜色支持，开不了就输出纯文本；窗口不少于 64 列时品牌名排成一行，44–63 列时 BAILU 缩为小字标签放在 DEPLOYPILOT 上方，更窄时显示单行文字标题；窗口高度放不下启动画面时不播放动画；方块字符显示为双倍宽度时改用色块二维码；二维码比窗口宽或无法显示时只给出链接。窗口够宽时配对说明显示在二维码右侧；二维码固定白底黑块，浅色终端主题下也能扫描。
- Windows 上的 AI 助手通过 PowerShell 执行命令，但内置的部署知识（apt、vLLM 等）面向 Linux，Windows 下的自动部署能力有限。
- 命令黑名单只是安全兜底，不能替代沙箱或权限隔离。
