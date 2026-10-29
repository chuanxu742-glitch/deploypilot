# 隧道稳定性

2026-10-29，用户要求检查并做好内网穿透稳定性。目标仓 E:/WORK/lulu/deploypilot，沿用 main 工作区已有改动，本轮仅本地修改、测试和构建，不提交或发布，不中断现有运行实例。

当前代码使用免账号 Quick Tunnel。已查实下载有校验和慢速镜像回退、WebSocket 有 ping；缺少进程退出后的应用级恢复、首次连接确认和 pong 超时。stdout/stderr 顺序读取有阻塞风险，响应头卡住不受下载速率监测约束。长期在线率没有观测数据。

实现选择：保留现有临时接入方式，补足可恢复故障的处理。重建 Quick Tunnel 会换域名，需撤销旧会话并重新显示配对链接；不会恢复执行已中断的命令。短暂网络抖动沿用 cloudflared 自身重连。正式隧道、固定域名、系统服务和长期在线率监控不在本轮范围。

依据：[Quick Tunnels](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/) 明确无在线时间保证、每次创建改变 hostname；[运行参数](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/run-parameters/) 定义 pidfile 在首次连接成功后写入、auto 协议回退及内置重试。使用本机固定版本 2026.10.5 的 --help 复核参数。

计划见 implementation-plan.md；原始检查、备份与日志在 .probe-tmp/tunnel-stability-20261029/。验证见 verification.md。当前阶段：本地实现、回归和构建完成，未发布或替换现有实例，无仍在运行的验证任务。

结论：代码层面的启动、恢复、下载和失联清理已补强。公网两次短测出现一次 WebSocket 握手 EOF、一次 30 秒以上往返成功，前一次失败原因未确定，不能据后一次通过宣称链路稳定。长期生产接入仍需固定域名的正式隧道及目标网络上的持续观测；本次没有操作账号、域名或系统网络。
