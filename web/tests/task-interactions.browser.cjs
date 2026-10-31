const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const { mkdirSync } = require("node:fs");
const path = require("node:path");
const { spawn } = require("node:child_process");

(async () => {
  const root = path.resolve(__dirname, "../..");
  const previewPort = process.env.DP_TASK_PREVIEW_PORT || "4176";
  const previewArgs = [
    path.join(
      path.dirname(require.resolve("vite/package.json")),
      "bin/vite.js",
    ),
    ...(process.env.DP_TASK_DEV ? ["--clearScreen", "false"] : ["preview"]),
    "--host",
    "127.0.0.1",
    "--port",
    previewPort,
    "--strictPort",
  ];
  if (process.env.DP_TASK_PREVIEW_DIR)
    previewArgs.push("--outDir", path.resolve(process.env.DP_TASK_PREVIEW_DIR));
  const preview = spawn(process.execPath, previewArgs, {
    cwd: path.join(root, "web"),
    stdio: "ignore",
  });
  let browser;
  try {
    let ready = false;
    for (let i = 0; i < 100; i++) {
      try {
        if ((await fetch(`http://127.0.0.1:${previewPort}`)).ok) {
          ready = true;
          break;
        }
      } catch {}
      await new Promise((r) => setTimeout(r, 50));
    }
    if (!ready)
      throw new Error(`Preview server did not start on ${previewPort}`);
    browser = await chromium.launch({ channel: "chrome", headless: true });
    const record = (id, phase = "finished", events = []) => ({
      id,
      title: `任务 ${id}`,
      startedAt: Date.now() - 10000,
      endedAt: Date.now(),
      phase,
      events,
    });
    async function setup(
      records = [record("latest"), record("A"), record("B")],
      options = {},
    ) {
      const context = await browser.newContext();
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const s = {
        records,
        delays: {},
        sockets: [],
        messages: [],
        starts: [],
        current: null,
        state: "idle",
        autoAccept: true,
        statusReads: 0,
        taskReads: 0,
        holdFirstSnapshot: false,
        continuable: undefined,
      };
      s.snapshot = (socket = s.sockets.at(-1), extra = {}) =>
        socket?.send(
          JSON.stringify({
            type: "snapshot",
            state: s.state,
            task: s.current,
            tasks: s.records.map(({ events, ...r }) => r),
            continuable: s.continuable,
            ...extra,
          }),
        );
      await page.route("**/api/**", async (route) => {
        const p = new URL(route.request().url()).pathname;
        if (p.startsWith("/api/tasks/")) {
          s.taskReads++;
          const id = p.split("/").at(-1);
          if (s.delays[id]) await s.delays[id];
          const r = s.records.find((x) => x.id === id);
          if (r && id !== "missing") await route.fulfill({ json: r });
          else await route.fulfill({ status: 503, body: "unavailable" });
          return;
        }
        if (p === "/api/llm/status") {
          s.statusReads++;
          if (options.statusWait) await options.statusWait;
          if (options.statusCode) {
            await route.fulfill({
              status: options.statusCode,
              body: "model status unavailable",
            });
            return;
          }
        }
        const data =
          p === "/api/tasks"
            ? s.records.map(({ events, ...r }) => r)
            : p === "/api/llm/status"
              ? { hasKey: true, model: "test" }
              : p === "/api/llm/models"
                ? { models: [] }
                : {
                    hostname: "node",
                    platform: "linux",
                    username: "test",
                    cwd: "/",
                    version: "test",
                  };
        await route.fulfill({ json: data });
      });
      await page.routeWebSocket("**/ws/term", (sock) => {
        sock.send(JSON.stringify({ type: "agent_state", state: "idle" }));
        sock.onMessage(() => {});
      });
      await page.routeWebSocket("**/ws/agent", (sock) => {
        s.sockets.push(sock);
        if (!s.holdFirstSnapshot) s.snapshot(sock);
        sock.onMessage((raw) => {
          const m = JSON.parse(String(raw));
          s.messages.push(m);
          if (m.type === "start") {
            s.starts.push(m);
            if (s.autoAccept) {
              s.current = {
                ...record(`new-${s.starts.length}`, "running", [
                  {
                    id: 1,
                    taskId: `new-${s.starts.length}`,
                    type: "user",
                    text: m.task,
                  },
                ]),
                requestId: m.requestId,
              };
              s.records.unshift(s.current);
              s.state = "running";
              s.snapshot(sock);
            }
          }
        });
      });
      await page.goto(`http://127.0.0.1:${previewPort}`);
      await page.locator("#task-input").waitFor();
      if (records.length)
        await page
          .locator(".session-item")
          .first()
          .waitFor({ state: "attached" });
      await page.waitForTimeout(100);
      return { page, context, s };
    }
    const tests = [];
    const test = (name, fn) => tests.push([name, fn]);
    const sessionItem = (page, id) =>
      page.locator(".session-item").filter({ hasText: `任务 ${id}` });
    test("stale history response cannot overwrite newer selection", async () => {
      const { page, context, s } = await setup();
      let release;
      s.delays.A = new Promise((resolve) => {
        release = resolve;
      });
      await sessionItem(page, "A").click();
      await sessionItem(page, "B").click();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 B" })
        .waitFor();
      release();
      await page.waitForTimeout(120);
      assert.match(await page.locator("#agent-title").innerText(), /任务 B/);
      await context.close();
    });
    test("new task and its draft invalidate a delayed history read", async () => {
      const { page, context, s } = await setup();
      let release;
      s.delays.A = new Promise((resolve) => {
        release = resolve;
      });
      await sessionItem(page, "A").click();
      await page.getByRole("button", { name: "新建会话" }).click();
      await page.locator("#task-input").fill("新会话草稿");
      release();
      await page.waitForTimeout(120);
      assert.equal(
        await page.locator("#task-input").inputValue(),
        "新会话草稿",
      );
      assert.equal(await page.locator("#agent-title").innerText(), "部署任务");
      await context.close();
    });
    test("start stays disabled until this socket receives its first snapshot", async () => {
      const { page, context, s } = await setup([record("latest")]);
      s.continuable = "latest";
      // Setup already consumed a snapshot; force a fresh socket generation.
      s.holdFirstSnapshot = true;
      s.sockets.at(-1).close();
      await page.waitForTimeout(500);
      await page.locator("#task-input").fill("等待快照");
      const button = page.getByRole("button", {
        name: "执行任务",
        exact: true,
      });
      assert.equal(await button.isDisabled(), true);
      await page.locator("#task-input").press("Enter");
      assert.equal(s.starts.length, 0);
      s.snapshot();
      await page.waitForTimeout(100);
      assert.equal(await button.isDisabled(), false);
      await context.close();
    });
    test("draft is restored when switching between histories", async () => {
      const { page, context } = await setup([record("latest"), record("A")]);
      const history = (id) => sessionItem(page, id);
      const input = page.locator("#task-input");
      await input.fill("A草稿");
      await history("A").click();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 A" })
        .waitFor();
      await input.fill("A任务草稿");
      await history("latest").click();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 latest" })
        .waitFor();
      assert.equal(await input.inputValue(), "A草稿");
      await history("A").click();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 A" })
        .waitFor();
      assert.equal(await input.inputValue(), "A任务草稿");
      await context.close();
    });
    test("failed history load keeps draft and retry repeats history GET", async () => {
      const { page, context, s } = await setup([
        record("latest"),
        record("missing"),
      ]);
      const input = page.locator("#task-input");
      await input.fill("保留我");
      await sessionItem(page, "missing").click();
      await page.waitForTimeout(300);
      assert.match(
        await page.locator(".composer").innerText(),
        /任务记录读取失败/,
      );
      const before = s.taskReads;
      const statusBefore = s.statusReads;
      await page.getByRole("button", { name: "重试" }).click();
      await page.waitForTimeout(100);
      assert.equal(await input.inputValue(), "保留我");
      assert.ok(s.taskReads > before);
      assert.equal(s.statusReads, statusBefore);
      await context.close();
    });
    test("idle snapshot preserves selected history while busy history remains browsable", async () => {
      const old = record("old", "interrupted");
      const { page, context, s } = await setup([record("latest"), old]);
      await sessionItem(page, "old").click();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 old" })
        .waitFor();
      s.state = "idle";
      s.snapshot();
      s.sockets
        .at(-1)
        .send(JSON.stringify({ type: "run_state", state: "idle" }));
      await page.waitForTimeout(100);
      assert.match(
        await page.locator(".run-status [role=status]").innerText(),
        /进程重启后中断/,
      );
      s.state = "running";
      s.current = record("active", "running");
      s.snapshot();
      await page.locator(".composer-context").waitFor();
      await page.waitForTimeout(100);
      assert.equal(
        await page.locator(".session-item").first().isDisabled(),
        false,
      );
      await context.close();
    });
    test("a late model-status success cannot remove a history error's retry", async () => {
      let releaseStatus;
      const statusWait = new Promise((resolve) => {
        releaseStatus = resolve;
      });
      const { page, context, s } = await setup(
        [record("latest"), record("missing")],
        { statusWait },
      );
      await sessionItem(page, "missing").click();
      await page
        .getByRole("alert")
        .filter({ hasText: "任务记录读取失败" })
        .waitFor();
      releaseStatus();
      await page.locator(".model-button").filter({ hasText: "test" }).waitFor();
      assert.equal(
        await page.getByRole("button", { name: "重试", exact: true }).count(),
        1,
      );
      const taskReads = s.taskReads;
      await page.getByRole("button", { name: "重试", exact: true }).click();
      await page.waitForTimeout(100);
      assert.ok(s.taskReads > taskReads);
      assert.equal(s.statusReads, process.env.DP_TASK_DEV ? 2 : 1);
      await context.close();
    });
    test("a history success cannot remove a later model-status error's retry", async () => {
      let releaseStatus, releaseHistory;
      const statusWait = new Promise((resolve) => {
        releaseStatus = resolve;
      });
      const { page, context, s } = await setup(
        [record("latest"), record("A")],
        { statusWait, statusCode: 503 },
      );
      s.delays.A = new Promise((resolve) => {
        releaseHistory = resolve;
      });
      await sessionItem(page, "A").click();
      releaseStatus();
      await page
        .getByRole("alert")
        .filter({ hasText: "模型配置读取失败" })
        .waitFor();
      releaseHistory();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 A" })
        .waitFor();
      assert.equal(
        await page.getByRole("button", { name: "重试", exact: true }).count(),
        1,
      );
      const statusReads = s.statusReads;
      const taskReads = s.taskReads;
      await page.getByRole("button", { name: "重试", exact: true }).click();
      await page.waitForTimeout(100);
      assert.ok(s.statusReads > statusReads);
      assert.equal(s.taskReads, taskReads);
      await context.close();
    });
    test("busy draft is identified as a same-session supplementary message", async () => {
      const { page, context, s } = await setup([record("A")]);
      await page.getByRole("button", { name: "新建会话" }).click();
      await page.locator("#task-input").fill("新会话草稿");
      s.state = "running";
      s.current = record("active", "running");
      s.snapshot();
      assert.equal(
        await page.locator("#agent-panel").getAttribute("data-empty"),
        "false",
      );
      assert.match(
        await page.locator("#agent-title").innerText(),
        /任务 active/,
      );
      assert.equal(
        await page.getByRole("button", { name: "停止任务" }).isVisible(),
        true,
      );
      assert.equal(
        await page.locator("#task-input").inputValue(),
        "新会话草稿",
      );
      assert.match(
        await page.locator(".composer-context").innerText(),
        /新会话/,
      );
      assert.match(
        await page.locator(".composer-context").innerText(),
        /不会发给后台任务/,
      );
      await context.close();
    });
    test("unconfirmed start is retained across reconnect without automatic resend or target drift", async () => {
      const { page, context, s } = await setup([record("previous")]);
      s.autoAccept = false;
      await page.getByRole("button", { name: "新建会话", exact: true }).click();
      const input = page.locator("#task-input");
      await input.fill("不丢失的部署指令");
      await page.getByRole("button", { name: "执行任务", exact: true }).click();
      assert.ok(s.starts[0].requestId);
      await input.fill("发送后新写的草稿");
      assert.equal(await input.inputValue(), "发送后新写的草稿");
      s.sockets.at(-1).close();
      await page.waitForTimeout(900);
      assert.ok(s.sockets.length >= 2);
      assert.equal(s.starts.length, 1);
      assert.equal(await input.inputValue(), "发送后新写的草稿");
      const recovery = page.locator(".composer .feedback[role=status]");
      assert.match(await recovery.innerText(), /发送后新写的草稿/);
      await recovery.getByRole("button", { name: "载入待确认文本" }).click();
      assert.match(await recovery.innerText(), /不丢失的部署指令/);
      assert.equal(await input.inputValue(), "不丢失的部署指令");
      assert.doesNotMatch(
        await page.locator("#agent-title").innerText(),
        /任务 previous/,
      );
      await context.close();
    });
    test("accepted start is recognized by requestId on reconnect and keeps a later draft", async () => {
      const { page, context, s } = await setup([record("previous")]);
      await page.getByRole("button", { name: "新建会话", exact: true }).click();
      const input = page.locator("#task-input");
      await input.fill("accepted");
      await page.getByRole("button", { name: "执行任务", exact: true }).click();
      assert.ok(s.starts[0].requestId);
      await input.fill("后续草稿");
      s.sockets.at(-1).close();
      await page.waitForTimeout(800);
      assert.equal(s.starts.length, 1);
      assert.match(await page.locator("#agent-title").innerText(), /new-1/);
      assert.equal(await input.inputValue(), "后续草稿");
      await context.close();
    });
    test("a late accepted request found in task summaries clears only its untouched automatic restoration", async () => {
      const { page, context, s } = await setup([record("previous")]);
      s.autoAccept = false;
      await page.getByRole("button", { name: "新建会话", exact: true }).click();
      const input = page.locator("#task-input");
      await input.fill("late accepted");
      await page.getByRole("button", { name: "执行任务", exact: true }).click();
      const requestId = s.starts[0].requestId;
      s.sockets.at(-1).close();
      await page.waitForTimeout(800);
      assert.equal(await input.inputValue(), "late accepted");
      s.records.unshift({ ...record("late", "finished"), requestId });
      s.snapshot();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 late" })
        .waitFor();
      await page.waitForTimeout(100);
      assert.equal(await input.inputValue(), "");
      assert.equal(s.starts.length, 1);
      await context.close();
    });
    test("late acceptance keeps a draft that was edited and then restored to the original text", async () => {
      const { page, context, s } = await setup([record("previous")]);
      s.autoAccept = false;
      await page.getByRole("button", { name: "新建会话", exact: true }).click();
      const input = page.locator("#task-input");
      await input.fill("request text");
      await page.getByRole("button", { name: "执行任务", exact: true }).click();
      const requestId = s.starts[0].requestId;
      s.sockets.at(-1).close();
      await page.waitForTimeout(800);
      await input.fill("edited");
      await input.fill("request text");
      s.records.unshift({ ...record("late", "finished"), requestId });
      s.snapshot();
      await page.waitForTimeout(100);
      assert.equal(await input.inputValue(), "request text");
      assert.equal(s.starts.length, 1);
      await context.close();
    });
    test("late accepted summary binds the newer draft to its request task, not another task", async () => {
      const { page, context, s } = await setup([record("previous")]);
      s.autoAccept = false;
      await page.getByRole("button", { name: "新建会话", exact: true }).click();
      const input = page.locator("#task-input");
      await input.fill("请求 A");
      await page.getByRole("button", { name: "执行任务", exact: true }).click();
      const requestId = s.starts[0].requestId;
      s.sockets.at(-1).close();
      await page.waitForTimeout(800);
      await input.fill("A的补充草稿 X");
      s.records.unshift({ ...record("A", "finished"), requestId });
      s.records.unshift(record("B", "finished"));
      s.snapshot();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 A" })
        .waitFor();
      assert.equal(await input.inputValue(), "A的补充草稿 X");
      assert.equal(s.starts.length, 1);
      await context.close();
    });
    test("late confirmation after navigating to another history keeps that history's draft in place", async () => {
      const { page, context, s } = await setup([
        record("previous"),
        record("B"),
      ]);
      s.autoAccept = false;
      await page.getByRole("button", { name: "新建会话", exact: true }).click();
      const input = page.locator("#task-input");
      await input.fill("请求 A");
      await page.getByRole("button", { name: "执行任务", exact: true }).click();
      const requestId = s.starts[0].requestId;
      s.sockets.at(-1).close();
      await page.waitForTimeout(1000);
      assert.ok(s.sockets.length >= 2);
      await sessionItem(page, "B").click();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 B" })
        .waitFor();
      await input.fill("B的草稿");
      s.records.unshift({ ...record("A", "finished"), requestId });
      s.snapshot();
      await page.waitForTimeout(120);
      assert.match(await page.locator("#agent-title").innerText(), /任务 B/);
      assert.equal(await input.inputValue(), "B的草稿");
      await sessionItem(page, "A").click();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 A" })
        .waitFor();
      assert.equal(await input.inputValue(), "请求 A");
      await context.close();
    });
    test("continuation pending text stays with its original history after an idle reconnect", async () => {
      const { page, context, s } = await setup([record("A")]);
      s.continuable = "A";
      s.snapshot();
      await page
        .locator("#agent-title")
        .filter({ hasText: "任务 A" })
        .waitFor();
      s.autoAccept = false;
      const input = page.locator("#task-input");
      await input.fill("延续 A 的请求");
      await page.getByRole("button", { name: "执行任务", exact: true }).click();
      assert.equal(s.starts[0].continueFrom, "A");
      s.sockets.at(-1).close();
      await page.waitForTimeout(800);
      assert.equal(await input.inputValue(), "延续 A 的请求");
      assert.match(await page.locator("#agent-title").innerText(), /任务 A/);
      assert.equal(s.starts.length, 1);
      await context.close();
    });
    test("explicit rejection preserves pending text and a newer draft in the owning session", async () => {
      const { page, context, s } = await setup([record("previous")]);
      s.autoAccept = false;
      await page.getByRole("button", { name: "新建会话", exact: true }).click();
      const input = page.locator("#task-input");
      await input.fill("被拒绝的提交");
      await page.getByRole("button", { name: "执行任务", exact: true }).click();
      const requestId = s.starts[0].requestId;
      await input.fill("拒绝后写的新草稿");
      s.sockets
        .at(-1)
        .send(JSON.stringify({ type: "error", requestId, text: "模型繁忙" }));
      await page.waitForTimeout(100);
      assert.equal(await input.inputValue(), "拒绝后写的新草稿");
      const recovery = page.locator(".composer .feedback[role=status]");
      assert.match(await recovery.innerText(), /拒绝后写的新草稿/);
      await recovery.getByRole("button", { name: "载入待确认文本" }).click();
      assert.equal(await input.inputValue(), "被拒绝的提交");
      await recovery.getByRole("button", { name: "切回当前草稿" }).click();
      assert.equal(await input.inputValue(), "拒绝后写的新草稿");
      await context.close();
    });
    test("IME, busy Enter, stop cleanup and idle snapshot recovery remain intact", async () => {
      const { page, context, s } = await setup([record("A", "running")]);
      s.state = "running";
      s.current = s.records[0];
      s.snapshot();
      const input = page.locator("#task-input");
      await input.fill("行一");
      await input.press("Shift+Enter");
      await input.press("End");
      await input.press("Enter");
      assert.equal(s.starts.length, 0);
      assert.match(await input.inputValue(), /行一\n/);
      await page.evaluate(() => {
        const el = document.querySelector("#task-input");
        el.dispatchEvent(
          new KeyboardEvent("keydown", {
            key: "Enter",
            bubbles: true,
            keyCode: 229,
          }),
        );
      });
      assert.equal(s.starts.length, 0);
      await page.getByRole("button", { name: "停止任务" }).click();
      assert.ok(s.messages.some((message) => message.type === "stop"));
      s.state = "idle";
      s.current.phase = "finished";
      s.snapshot();
      await page.waitForTimeout(150);
      assert.equal(await input.isVisible(), true);
      await context.close();
    });
    test("wait_for activity refreshes its card and keeps condition output expandable", async () => {
      const { page, context, s } = await setup([]);
      const taskId = "wait-task";
      const callId = "wait-call";
      const current = record(taskId, "running", [
        { id: 1, taskId, type: "user", text: "等待服务就绪" },
        {
          id: 2,
          taskId,
          type: "tool_call",
          tool: "wait_for",
          callId,
          step: 1,
        },
        {
          id: 3,
          taskId,
          type: "activity",
          state: "waiting",
          tool: "wait_for",
          callId,
          text: "等待 HTTP 服务，已等待 5 秒",
        },
      ]);
      s.current = current;
      s.records.unshift(current);
      s.state = "running";
      s.snapshot();
      await page
        .locator(".action-progress")
        .getByText("等待 HTTP 服务，已等待 5 秒", { exact: true })
        .waitFor();
      await page
        .locator(".run-status [role=status]")
        .getByText("等待 HTTP 服务，已等待 5 秒", { exact: true })
        .waitFor();

      const socket = s.sockets.at(-1);
      socket.send(
        JSON.stringify({
          id: 4,
          taskId,
          type: "activity",
          state: "waiting",
          tool: "wait_for",
          callId,
          text: "等待 HTTP 服务，已等待 10 秒；最近观察：连接被拒绝",
        }),
      );
      await page
        .locator(".action-progress")
        .getByText("已等待 10 秒；最近观察：连接被拒绝", { exact: false })
        .waitFor();
      assert.equal(await page.locator(".action-progress").count(), 1);
      assert.match(
        await page.locator(".run-status [role=status]").innerText(),
        /已等待 10 秒/,
      );

      socket.send(
        JSON.stringify({
          id: 5,
          taskId,
          type: "output",
          callId,
          text: "HTTP 200，条件已满足",
        }),
      );
      socket.send(
        JSON.stringify({
          id: 6,
          taskId,
          type: "tool_done",
          callId,
          exitCode: 0,
          durationMs: 10000,
        }),
      );
      const action = page.locator(".action").first();
      await action.locator(".chip").filter({ hasText: "退出码 0" }).waitFor();
      await action.locator("summary").click();
      await action.locator(".action-output pre").waitFor({ state: "visible" });
      assert.match(
        await action.locator(".action-output pre").innerText(),
        /HTTP 200，条件已满足/,
      );
      await context.close();
    });
    test("long running task keeps the composer compact and shows command timing and outcomes", async () => {
      const { page, context, s } = await setup([]);
      await page.setViewportSize({ width: 1280, height: 720 });
      const taskId = "long-command-task";
      const longTitle = `PROMPT-HEAD ${"检查当前部署环境并逐步收集详细证据。".repeat(1400)} PROMPT-TAIL`;
      const now = Date.now();
      const current = record(taskId, "running", [
        { id: 1, taskId, type: "user", text: longTitle },
        ...Array.from({ length: 9 }, (_, i) => ({
          id: i + 2,
          taskId,
          type: "status",
          step: i + 1,
          text: `正在执行步骤 ${i + 1}`,
        })),
        {
          id: 11,
          taskId,
          type: "tool_call",
          tool: "run_command",
          callId: "cancel-command",
          command: "curl -fsS http://127.0.0.1/ready",
          timestamp: now - 374000,
          timeoutSeconds: 600,
        },
      ]);
      current.title = longTitle;
      s.current = current;
      s.records.unshift(current);
      s.state = "running";
      s.snapshot();
      const screenshotDir = path.join(root, ".probe-tmp", "ui-screenshots");
      mkdirSync(screenshotDir, { recursive: true });
      await page.screenshot({
        path: path.join(screenshotDir, "codex-long-task.png"),
        fullPage: true,
      });
      const input = page.locator("#task-input");
      await input.waitFor();
      const runningMeta = page.locator(".action-running-meta");
      await runningMeta.getByText(/已运行/).waitFor();
      const contextText = await page.locator(".composer-context").innerText();
      assert.ok(!contextText.includes("PROMPT-TAIL"));
      assert.ok(
        (await page.locator(".composer-context").boundingBox()).height < 100,
      );
      assert.ok(
        (await page.locator(".agent-scroll").boundingBox()).height > 100,
      );
      assert.match(await runningMeta.innerText(), /已运行 6分\d+秒/);
      assert.match(await runningMeta.innerText(), /限时 10分0秒/);
      await page.setViewportSize({ width: 390, height: 844 });
      assert.ok(
        await runningMeta.evaluate((el) => el.scrollWidth <= el.clientWidth),
        "elapsed time and timeout remain inside the command card on a narrow viewport",
      );

      const socket = s.sockets.at(-1);
      socket.send(
        JSON.stringify({
          id: 12,
          taskId,
          type: "tool_done",
          tool: "run_command",
          callId: "cancel-command",
          exitCode: -1,
          state: "cancelled",
          durationMs: 374000,
          text: "命令已取消。完整输出已保存。",
          logPath: "C:/private/command.log",
        }),
      );
      const cancelled = page
        .locator(".tl-action")
        .filter({ hasText: "已取消" });
      await cancelled.waitFor();
      assert.equal(await cancelled.locator(".tl-error").count(), 0);
      assert.equal(
        await cancelled.locator(".action-duration").innerText(),
        "6分14秒",
      );
      assert.equal(
        await cancelled.locator(".action-log-path code").innerText(),
        "C:/private/command.log",
      );
      assert.equal(
        await input.getAttribute("placeholder"),
        "补充当前任务，点击发送后在当前执行步骤结束后应用",
      );
      assert.equal(
        await page.getByRole("button", { name: "停止任务" }).isEnabled(),
        true,
        "tool cancellation does not release the task before cleanup acknowledgement",
      );

      socket.send(
        JSON.stringify({
          id: 13,
          taskId,
          type: "tool_call",
          tool: "run_command",
          callId: "timeout-command",
          command: "sleep 5",
          timestamp: Date.now(),
          timeoutSeconds: 2,
        }),
      );
      socket.send(
        JSON.stringify({
          id: 14,
          taskId,
          type: "error",
          callId: "timeout-command",
          state: "timeout",
          text: "命令已超过 2 秒时限",
          logPath: "C:/private/timeout.log",
        }),
      );
      const timeout = page.locator(".tl-action").filter({ hasText: "超时" });
      await timeout.waitFor();
      assert.equal(await timeout.locator('.chip[data-tone="bad"]').count(), 1);
      assert.equal(
        await timeout.locator(".action-log-path code").innerText(),
        "C:/private/timeout.log",
      );

      socket.send(
        JSON.stringify({
          id: 15,
          taskId,
          type: "tool_call",
          tool: "run_command",
          callId: "ordinary-failure",
          command: "false",
          timestamp: Date.now(),
        }),
      );
      socket.send(
        JSON.stringify({
          id: 16,
          taskId,
          type: "tool_done",
          callId: "ordinary-failure",
          exitCode: -1,
          durationMs: 1000,
          text: "命令输出包含 context canceled，但后台没有标记任务取消。",
        }),
      );
      const ordinaryFailure = page
        .locator(".tl-action")
        .filter({ hasText: "退出码 -1" });
      await ordinaryFailure.waitFor();
      assert.equal(
        await ordinaryFailure.locator('.chip[data-tone="bad"]').count(),
        1,
      );

      socket.send(
        JSON.stringify({
          id: 17,
          taskId,
          type: "tool_call",
          tool: "run_command",
          callId: "legacy-cancel",
          command: "curl old-endpoint",
          timestamp: Date.now(),
        }),
      );
      socket.send(
        JSON.stringify({
          id: 18,
          taskId,
          type: "error",
          callId: "legacy-cancel",
          text: "exit=-1 error=command cancelled: context canceled output:[full output: C:/private/legacy.log]",
        }),
      );
      const legacyCancelled = page
        .locator(".tl-action")
        .filter({ hasText: "C:/private/legacy.log" });
      await legacyCancelled.waitFor();
      assert.equal(await legacyCancelled.locator(".tl-error").count(), 0);
      assert.equal(
        await legacyCancelled.getByText("命令已取消", { exact: true }).count(),
        1,
      );
      assert.equal(
        await legacyCancelled.locator(".action-log-path code").innerText(),
        "C:/private/legacy.log",
      );
      await context.close();
    });
    let failures = 0;
    for (const [name, fn] of tests) {
      try {
        await fn();
        console.log(`PASS ${name}`);
      } catch (error) {
        failures++;
        console.error(`FAIL ${name}: ${error.stack || error}`);
      }
    }
    if (failures) process.exitCode = 1;
    else console.log(`${tests.length} task interaction checks passed`);
  } finally {
    await browser?.close();
    preview.kill();
  }
})();
