const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const { mkdirSync } = require("node:fs");
const path = require("node:path");
const { spawn } = require("node:child_process");

(async () => {
  const root = path.resolve(__dirname, "../..");
  const previewPort = process.env.DP_TASK_PREVIEW_PORT || "4385";
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
        sock.onClose(() => {
          s.sockets = s.sockets.filter((item) => item !== sock);
        });
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
    const artifacts = path.join(root, ".probe-tmp/agent-soak-20261030");
    const tests = [];
    const test = (name, run) => tests.push([name, run]);
    const emit = (s, e) => s.sockets.at(-1).send(JSON.stringify(e));
    async function running(options = {}) {
      const active = { ...record("A", "running"), endedAt: undefined };
      const past = { ...record("B"), canContinue: true };
      const x = await setup([active, past], options);
      x.page.setDefaultTimeout(2200);
      x.s.current = active;
      x.s.state = "running";
      x.s.snapshot();
      await x.page
        .getByRole("button", { name: "发送补充", exact: true })
        .waitFor();
      return x;
    }
    test("accepted supplement survives lost ack, clipping and a full browser refresh with readable content", async () => {
      const { page, context, s } = await running();
      try {
        const input = page.locator("#task-input");
        await input.fill("端口改为8080并保留已有数据库");
        await page
          .getByRole("button", { name: "发送补充", exact: true })
          .click();
        const sent = s.messages.find((m) => m.type === "steer");
        s.current.events = [
          {
            type: "user",
            id: 1,
            taskId: "A",
            requestId: sent.requestId,
            state: "queued",
            text: sent.task,
          },
          ...Array.from({ length: 511 }, (_, i) => ({
            type: "status",
            id: i + 2,
            taskId: "A",
            text: "执行进展 " + i,
          })),
        ];
        await page.reload();
        await page
          .getByRole("button", { name: "发送补充", exact: true })
          .waitFor();
        const card = page.locator(
          '[data-steer-request="' + sent.requestId + '"]',
        );
        assert.match(await card.innerText(), /已接收/);
        await page.screenshot({
          path: path.join(artifacts, "frontend-queued-clipped-green.png"),
          fullPage: true,
        });
        await card
          .locator("summary")
          .filter({ hasText: "查看补充内容" })
          .click();
        assert.match(await card.innerText(), /端口改为8080并保留已有数据库/);
        await page.screenshot({
          path: path.join(
            artifacts,
            "frontend-queued-clipped-expanded-green.png",
          ),
          fullPage: true,
        });
        assert.equal(s.messages.filter((m) => m.type === "steer").length, 1);
      } finally {
        await context.close();
      }
    });
    test("a delayed history response cannot downgrade an already applied supplement to deferred", async () => {
      const { page, context, s } = await running();
      try {
        const input = page.locator("#task-input");
        await input.fill("恢复后要执行的补充");
        await page
          .getByRole("button", { name: "发送补充", exact: true })
          .click();
        const sent = s.messages.find((m) => m.type === "steer");
        s.current.events = [
          {
            type: "user",
            id: 10,
            taskId: "A",
            requestId: sent.requestId,
            state: "queued",
            text: sent.task,
          },
          {
            type: "activity",
            id: 11,
            taskId: "A",
            requestId: sent.requestId,
            state: "steer_deferred",
            text: "补充尚未应用，手动继续后处理",
          },
        ];
        await page.locator('.session-item[data-task-id="B"]').click();
        await page.waitForTimeout(60);
        let release, started;
        const gate = new Promise((r) => (release = r));
        const requested = new Promise((r) => (started = r));
        await page.route("**/api/tasks/A", async (route) => {
          const stale = structuredClone(s.current);
          started();
          await gate;
          await route.fulfill({ json: stale });
        });
        await page
          .getByRole("button", { name: "返回正在执行的会话", exact: true })
          .click();
        await requested;
        emit(s, {
          type: "activity",
          id: 12,
          taskId: "A",
          requestId: sent.requestId,
          state: "steer_applied",
          text: "补充已应用",
        });
        await page.waitForTimeout(60);
        release();
        await page.waitForTimeout(100);
        const card = page.locator(
          '[data-steer-request="' + sent.requestId + '"]',
        );
        await page.screenshot({
          path: path.join(artifacts, "frontend-stale-deferred-green.png"),
          fullPage: true,
        });
        assert.match(await card.innerText(), /已应用到模型上下文/);
      } finally {
        await context.close();
      }
    });
    test("short mobile viewport keeps send reachable with multiline draft and several queued supplements", async () => {
      const { page, context, s } = await running({
        viewport: { width: 390, height: 520 },
      });
      try {
        s.current.events = Array.from({ length: 8 }, (_, i) => ({
          type: "user",
          id: i + 1,
          taskId: "A",
          requestId: "queued-" + i,
          state: "queued",
          text: "补充 #" + i,
        }));
        s.snapshot();
        const input = page.locator("#task-input");
        await input.fill(
          Array.from({ length: 10 }, (_, i) => "多行补充内容 " + i).join("\n"),
        );
        await input.dispatchEvent("keydown", {
          key: "Enter",
          isComposing: true,
          keyCode: 229,
        });
        assert.equal(s.messages.filter((m) => m.type === "steer").length, 0);
        const button = page.getByRole("button", {
          name: "发送补充",
          exact: true,
        });
        const rect = await button.boundingBox();
        await page.screenshot({
          path: path.join(artifacts, "frontend-mobile-short-green.png"),
          fullPage: true,
        });
        console.log("MOBILE_SEND_RECT " + JSON.stringify(rect));
        assert.ok(
          rect.y >= 0 && rect.y + rect.height <= 520,
          "send action must stay inside mobile viewport",
        );
        await button.click();
        assert.equal(s.messages.filter((m) => m.type === "steer").length, 1);
        const sent = s.messages.find((message) => message.type === "steer");
        emit(s, {
          type: "steer_ack",
          taskId: "A",
          requestId: sent.requestId,
          state: "queued",
        });
        await input.fill("手机A草稿");
        await page
          .getByRole("button", { name: "展开会话导航", exact: true })
          .click();
        await page.locator('.session-item[data-task-id="B"]').click();
        await page.waitForTimeout(60);
        await input.fill("手机B草稿");
        await input.dispatchEvent("keydown", {
          key: "Enter",
          isComposing: true,
          keyCode: 229,
        });
        assert.equal(
          s.messages.filter((message) => message.type === "steer").length,
          1,
        );
        await page
          .getByRole("button", { name: "返回正在执行的会话", exact: true })
          .click();
        await page.waitForTimeout(60);
        assert.equal(await input.inputValue(), "手机A草稿");
      } finally {
        await context.close();
      }
    });
    test("several supplements retain independent lifecycle states and A/B drafts", async () => {
      const { page, context, s } = await running();
      try {
        const input = page.locator("#task-input");
        const requests = [];
        for (const text of ["只改端口", "保留数据库", "先验证再完成"]) {
          await input.fill(text);
          await page
            .getByRole("button", { name: "发送补充", exact: true })
            .click();
          const sent = s.messages
            .filter((message) => message.type === "steer")
            .at(-1);
          requests.push(sent);
          emit(s, {
            type: "user",
            id: 50 + requests.length,
            taskId: "A",
            requestId: sent.requestId,
            state: "queued",
            text,
          });
          await page.waitForTimeout(60);
        }
        emit(s, {
          type: "activity",
          id: 60,
          taskId: "A",
          requestId: requests[0].requestId,
          state: "steer_applied",
          text: "已应用",
        });
        emit(s, {
          type: "activity",
          id: 61,
          taskId: "A",
          requestId: requests[1].requestId,
          state: "steer_deferred",
          text: "未应用",
        });
        await input.fill("A未发送草稿");
        await page.locator('.session-item[data-task-id="B"]').click();
        await page.waitForTimeout(60);
        await input.fill("B未发送草稿");
        emit(s, {
          type: "steer_ack",
          taskId: "A",
          requestId: requests[0].requestId,
          state: "queued",
        });
        s.snapshot();
        await page.waitForTimeout(60);
        assert.equal(await input.inputValue(), "B未发送草稿");
        assert.equal(await page.locator("[data-steer-request]").count(), 0);
        await page
          .getByRole("button", { name: "返回正在执行的会话", exact: true })
          .click();
        await page.waitForTimeout(60);
        assert.equal(await input.inputValue(), "A未发送草稿");
        const statuses = await Promise.all(
          requests.map((request) =>
            page
              .locator(`[data-steer-request="${request.requestId}"]`)
              .innerText(),
          ),
        );
        assert.match(statuses[0], /已应用/);
        assert.match(statuses[1], /尚未应用/);
        assert.match(statuses[2], /已接收/);
        assert.equal(
          s.messages.filter((message) => message.type === "steer").length,
          3,
        );
      } finally {
        await context.close();
      }
    });
    let failures = 0;
    for (const [name, run] of tests) {
      try {
        await run();
        console.log("PASS " + name);
      } catch (e) {
        failures++;
        console.error("FAIL " + name + ": " + e.stack);
      }
    }
    console.log(
      `${tests.length - failures}/${tests.length} soak checks passed`,
    );
    if (failures) process.exitCode = 1;
  } finally {
    await browser?.close();
    preview.kill();
  }
})();
