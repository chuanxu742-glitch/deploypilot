const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const path = require("node:path");
const {
  createSetup,
  record,
  runTests,
  startVite,
} = require("./support/mock-app.cjs");

(async () => {
  const root = path.resolve(__dirname, "../..");
  const vite = await startVite({ defaultPort: "4385" });
  let browser;
  try {
    browser = await chromium.launch({ channel: "chrome", headless: true });
    const setup = createSetup(browser, vite.url, { pruneClosedSockets: true });
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
    await runTests(
      tests,
      (passed, total) => `${passed}/${total} soak checks passed`,
    );
  } finally {
    await browser?.close();
    vite.stop();
  }
})();
