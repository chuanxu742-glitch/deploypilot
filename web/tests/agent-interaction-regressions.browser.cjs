const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const { mkdirSync } = require("node:fs");
const path = require("node:path");
const { spawn } = require("node:child_process");

(async () => {
  const root = path.resolve(__dirname, "../..");
  const previewPort = process.env.DP_TASK_PREVIEW_PORT || "4315";
  const previewArgs = [
    path.join(
      path.dirname(require.resolve("vite/package.json")),
      "bin/vite.js",
    ),
    "--clearScreen",
    "false",
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
      const context = await browser.newContext({
        viewport: options.viewport || { width: 1440, height: 960 },
      });
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
      // The first visit pays Vite's cold dependency optimization.
      await page.goto(`http://127.0.0.1:${previewPort}`, { timeout: 30000 });
      await page.locator("#task-input").waitFor({ timeout: 30000 });
      if (records.length)
        await page
          .locator(".session-item")
          .first()
          .waitFor({ state: "attached" });
      await page.waitForTimeout(100);
      return { page, context, s };
    }
    const tests = [];
    const test = (name, run) => tests.push([name, run]);
    const mobile = { viewport: { width: 390, height: 844 } };
    const openDrawer = (page) =>
      page.getByRole("button", { name: "展开会话导航", exact: true }).click();
    test("stopping another tab task releases selected history and terminal", async () => {
      const { page, context, s } = await setup([
        { ...record("A"), canContinue: true },
      ]);
      try {
        s.current = record("B", "running");
        s.records.unshift(s.current);
        s.state = "running";
        s.snapshot();
        await page
          .getByRole("button", { name: "停止任务", exact: true })
          .click();
        s.sockets.at(-1).send(
          JSON.stringify({
            type: "run_state",
            state: "stopping",
            taskId: "B",
          }),
        );
        s.current.phase = "finished";
        s.state = "idle";
        s.snapshot();
        s.sockets
          .at(-1)
          .send(
            JSON.stringify({ type: "run_state", state: "idle", taskId: "B" }),
          );
        await page.waitForTimeout(100);
        await page.locator("#task-input").fill("后续消息");
        assert.equal(
          await page
            .getByRole("button", { name: "执行任务", exact: true })
            .isDisabled(),
          false,
        );
        assert.equal(
          await page
            .getByRole("button", { name: "新建会话", exact: true })
            .isDisabled(),
          false,
        );
        assert.equal(
          await page.locator(".term").getAttribute("data-readonly"),
          "false",
        );
      } finally {
        await context.close();
      }
    });
    test("explicit new task preserves an unsent old history draft and the new task follow-up", async () => {
      const { page, context, s } = await setup([record("A")]);
      try {
        const input = page.locator("#task-input");
        await input.fill("旧历史尚未发送的草稿");
        assert.equal(
          await page
            .getByRole("button", { name: "执行任务", exact: true })
            .isDisabled(),
          true,
        );
        await page
          .getByRole("button", { name: "新建会话", exact: true })
          .click();
        await input.fill("独立部署指令");
        await page
          .getByRole("button", { name: "执行任务", exact: true })
          .click();
        await page.waitForTimeout(100);
        assert.equal(s.starts[0].continueFrom, undefined);
        const acceptedId = s.current.id;
        await input.fill("新任务稍后发送的补充");
        s.current.phase = "finished";
        s.state = "idle";
        s.snapshot();
        s.sockets.at(-1).send(
          JSON.stringify({
            type: "run_state",
            state: "idle",
            taskId: acceptedId,
          }),
        );
        await page.locator('.session-item[data-task-id="A"]').click();
        await page.waitForTimeout(100);
        assert.equal(await input.inputValue(), "旧历史尚未发送的草稿");
        await page
          .locator(`.session-item[data-task-id="${acceptedId}"]`)
          .click();
        await page.waitForTimeout(100);
        assert.equal(await input.inputValue(), "新任务稍后发送的补充");
      } finally {
        await context.close();
      }
    });
    test("recovery versions preserve edits independently in both buffers", async () => {
      const { page, context, s } = await setup([record("A")]);
      try {
        s.autoAccept = false;
        await page
          .getByRole("button", { name: "新建会话", exact: true })
          .click();
        const input = page.locator("#task-input");
        await input.fill("原始提交");
        await page
          .getByRole("button", { name: "执行任务", exact: true })
          .click();
        await input.fill("后续草稿");
        s.sockets.at(-1).send(
          JSON.stringify({
            type: "error",
            requestId: s.starts[0].requestId,
            text: "任务未启动，请重试",
          }),
        );
        const recovery = page.locator(".composer .feedback[role=status]");
        await recovery.getByRole("button", { name: "载入待确认文本" }).click();
        await input.fill("修订后的原始提交");
        await recovery.getByRole("button", { name: "切回当前草稿" }).click();
        assert.equal(await input.inputValue(), "后续草稿");
        assert.match(await recovery.innerText(), /后续草稿/);
        await input.fill("修订后的后续草稿");
        await recovery.getByRole("button", { name: "载入待确认文本" }).click();
        assert.equal(await input.inputValue(), "修订后的原始提交");
        await recovery.getByRole("button", { name: "切回当前草稿" }).click();
        assert.equal(await input.inputValue(), "修订后的后续草稿");
        await input.fill("");
        await recovery.getByRole("button", { name: "载入待确认文本" }).click();
        assert.equal(await input.inputValue(), "修订后的原始提交");
        await recovery.getByRole("button", { name: "切回当前草稿" }).click();
        assert.equal(await input.inputValue(), "");
      } finally {
        await context.close();
      }
    });
    test("mobile drawer close is clickable above panel tabs", async () => {
      const { page, context } = await setup([record("A")], mobile);
      try {
        await openDrawer(page);
        await page
          .getByRole("button", { name: "关闭会话栏", exact: true })
          .click({ timeout: 1500 });
        assert.equal(
          await page.locator(".app-body").getAttribute("data-sidebar-open"),
          "false",
        );
      } finally {
        await context.close();
      }
    });
    for (const action of ["history", "new"])
      test(`mobile ${action} selection returns to task view`, async () => {
        const { page, context } = await setup(
          [record("A"), record("B")],
          mobile,
        );
        try {
          await page
            .getByRole("button", { name: "远程终端", exact: true })
            .click();
          await openDrawer(page);
          if (action === "history")
            await page.locator('.session-item[data-task-id="B"]').click();
          else
            await page
              .getByRole("button", { name: "新建会话", exact: true })
              .click();
          assert.equal(
            await page.locator(".workspace").getAttribute("data-view"),
            "task",
          );
        } finally {
          await context.close();
        }
      });
    test("mobile drawer isolates keyboard focus and prevents background send", async () => {
      const { page, context, s } = await setup(
        [record("A"), record("B")],
        mobile,
      );
      try {
        const input = page.locator("#task-input");
        await input.fill("不能从抽屉后方发送");
        await openDrawer(page);
        assert.equal(
          await page.locator(".workspace").evaluate((el) => el.inert),
          true,
        );
        for (let i = 0; i < 10; i++) {
          await page.keyboard.press("Tab");
          assert.equal(
            await page.evaluate(
              () => !!document.activeElement.closest("#session-sidebar"),
            ),
            true,
          );
        }
        await input.evaluate((el) => el.focus());
        await page.keyboard.press("Enter");
        assert.equal(s.starts.length, 0);
        await page.keyboard.press("Escape");
        assert.equal(
          await page.locator(".app-body").getAttribute("data-sidebar-open"),
          "false",
        );
        assert.equal(
          await page
            .getByRole("button", { name: "展开会话导航", exact: true })
            .evaluate((el) => el === document.activeElement),
          true,
        );
      } finally {
        await context.close();
      }
    });
    test("live command output respects scroll pause and explicit resume", async () => {
      const { page, context, s } = await setup([record("A")]);
      try {
        s.current = {
          ...s.records[0],
          phase: "running",
          events: [
            {
              id: 1,
              taskId: "A",
              type: "tool_call",
              tool: "run_command",
              callId: "stream",
              command: "stream logs",
              timestamp: Date.now(),
            },
            {
              id: 2,
              taskId: "A",
              type: "output",
              callId: "stream",
              text: Array.from({ length: 100 }, (_, i) => `line ${i}`).join(
                "\n",
              ),
            },
          ],
        };
        s.state = "running";
        s.snapshot();
        const pre = page.locator(".action-output pre");
        await pre.waitFor();
        await pre.evaluate((el) => {
          el.scrollTop = 0;
          el.dispatchEvent(new Event("scroll"));
        });
        s.sockets.at(-1).send(
          JSON.stringify({
            id: 3,
            taskId: "A",
            type: "output",
            callId: "stream",
            text: "\nnew line",
          }),
        );
        await page.waitForTimeout(100);
        assert.equal(await pre.evaluate((el) => el.scrollTop), 0);
        await page
          .getByRole("button", { name: "返回最新输出", exact: true })
          .click();
        s.sockets.at(-1).send(
          JSON.stringify({
            id: 4,
            taskId: "A",
            type: "output",
            callId: "stream",
            text: "\nanother line",
          }),
        );
        await page.waitForTimeout(100);
        assert.equal(
          await pre.evaluate((el) => el.scrollTop),
          await pre.evaluate((el) => el.scrollHeight - el.clientHeight),
        );
      } finally {
        await context.close();
      }
    });
    let failed = 0;
    for (const [name, run] of tests) {
      try {
        await run();
        console.log("PASS " + name);
      } catch (e) {
        failed++;
        console.error("FAIL " + name + ": " + e.stack);
      }
    }
    console.log(`${tests.length - failed}/${tests.length} checks passed`);
    if (failed) process.exitCode = 1;
  } finally {
    await browser?.close();
    preview.kill();
  }
})();
