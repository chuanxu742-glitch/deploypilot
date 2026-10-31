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
  const vite = await startVite({ defaultPort: "4325" });
  let browser;
  try {
    browser = await chromium.launch({ channel: "chrome", headless: true });
    const setup = createSetup(browser, vite.url, { pruneClosedSockets: true });
    const tests = [];
    const test = (name, run) => tests.push([name, run]);
    const emit = (s, e) => s.sockets.at(-1).send(JSON.stringify(e));
    async function running() {
      const active = record("A", "running");
      const past = { ...record("B"), canContinue: true };
      const x = await setup([active, past]);
      x.page.setDefaultTimeout(1800);
      x.s.current = active;
      x.s.state = "running";
      x.s.snapshot();
      await x.page
        .getByRole("button", { name: "发送补充", exact: true })
        .waitFor();
      return x;
    }
    test("busy history browsing isolates events and both drafts and can return to live", async () => {
      const { page, context, s } = await running();
      try {
        const input = page.locator("#task-input");
        await input.fill("A草稿");
        await page.locator('.session-item[data-task-id="B"]').click();
        await page.waitForTimeout(60);
        await input.fill("B草稿");
        emit(s, { id: 20, taskId: "A", type: "thinking", text: "A独有进展" });
        s.snapshot();
        await page.waitForTimeout(60);
        assert.match(await page.locator("#agent-title").innerText(), /B/);
        assert.equal(await input.inputValue(), "B草稿");
        assert.doesNotMatch(
          await page.locator(".timeline").innerText(),
          /A独有进展/,
        );
        assert.equal(
          await page
            .getByRole("button", { name: "执行任务", exact: true })
            .isDisabled(),
          true,
        );
        await page
          .getByRole("button", { name: "返回正在执行的会话", exact: true })
          .click();
        await page.waitForTimeout(60);
        assert.equal(await input.inputValue(), "A草稿");
        await page.locator('.session-item[data-task-id="B"]').click();
        await page.waitForTimeout(60);
        assert.equal(await input.inputValue(), "B草稿");
      } finally {
        await context.close();
      }
    });
    test("server capabilities resume older A and B without changing identity", async () => {
      const { page, context, s } = await setup([
        { ...record("A"), canContinue: true },
        { ...record("B"), canContinue: true },
      ]);
      try {
        s.autoAccept = false;
        await page.locator('.session-item[data-task-id="B"]').click();
        await page.waitForTimeout(60);
        await page.locator("#task-input").fill("继续B");
        await page
          .getByRole("button", { name: "执行任务", exact: true })
          .click();
        assert.equal(s.starts[0].continueFrom, "B");
        emit(s, {
          type: "error",
          requestId: s.starts[0].requestId,
          text: "暂不可用",
        });
        await page.waitForTimeout(50);
        await page.locator('.session-item[data-task-id="A"]').click();
        await page.waitForTimeout(60);
        await page.locator("#task-input").fill("继续A");
        await page
          .getByRole("button", { name: "执行任务", exact: true })
          .click();
        assert.equal(s.starts[1].continueFrom, "A");
      } finally {
        await context.close();
      }
    });
    test("unavailable old conversation blocks accidental new task and requires explicit new", async () => {
      const { page, context, s } = await setup([
        { ...record("A"), canContinue: false },
      ]);
      try {
        s.continuable = "A";
        s.snapshot();
        await page.waitForTimeout(60);
        await page.locator("#task-input").fill("回答旧问题");
        assert.equal(
          await page
            .getByRole("button", { name: "执行任务", exact: true })
            .isDisabled(),
          true,
        );
        await page.locator("#task-input").press("Enter");
        assert.equal(s.starts.length, 0);
        assert.match(
          await page.locator(".composer").innerText(),
          /无法继续|不能继续/,
        );
        await page
          .getByRole("button", { name: "新建会话", exact: true })
          .click();
        await page.locator("#task-input").fill("独立任务");
        await page
          .getByRole("button", { name: "执行任务", exact: true })
          .click();
        assert.equal(s.starts[0].continueFrom, undefined);
      } finally {
        await context.close();
      }
    });
    test("steer is explicit, idempotent in UI, waits for applied, preserves IME and empty guards", async () => {
      const { page, context, s } = await running();
      try {
        const input = page.locator("#task-input");
        const send = page.getByRole("button", {
          name: "发送补充",
          exact: true,
        });
        await input.fill("  ");
        assert.equal(await send.isDisabled(), true);
        await input.fill("改用8080");
        await input.dispatchEvent("keydown", {
          key: "Enter",
          isComposing: true,
          keyCode: 229,
        });
        await input.press("Enter");
        assert.equal(s.messages.filter((m) => m.type === "steer").length, 0);
        await input.fill("改用8080");
        await send.click();
        const sent = s.messages.find((m) => m.type === "steer");
        assert.equal(sent.taskId, "A");
        assert.equal(sent.task, "改用8080");
        await input.fill("新草稿");
        emit(s, {
          type: "steer_ack",
          taskId: "A",
          requestId: sent.requestId,
          state: "queued",
        });
        emit(s, {
          type: "user",
          id: 30,
          taskId: "A",
          requestId: sent.requestId,
          state: "queued",
          text: sent.task,
        });
        emit(s, {
          type: "steer_ack",
          taskId: "A",
          requestId: sent.requestId,
          state: "queued",
        });
        await page.waitForTimeout(80);
        assert.equal(
          await page
            .locator('[data-steer-request="' + sent.requestId + '"]')
            .count(),
          1,
        );
        assert.match(
          await page
            .locator('[data-steer-request="' + sent.requestId + '"]')
            .innerText(),
          /当前执行步骤结束后应用|等待应用/,
        );
        emit(s, {
          type: "activity",
          id: 31,
          taskId: "A",
          requestId: sent.requestId,
          state: "steer_applied",
          text: "已应用补充",
        });
        emit(s, {
          type: "steer_ack",
          taskId: "A",
          requestId: sent.requestId,
          state: "queued",
        });
        await page.waitForTimeout(60);
        assert.match(
          await page
            .locator('[data-steer-request="' + sent.requestId + '"]')
            .innerText(),
          /已应用/,
        );
        assert.equal(await input.inputValue(), "新草稿");
      } finally {
        await context.close();
      }
    });
    test("pending steer stays with the active task while another history owns its draft", async () => {
      const { page, context, s } = await running();
      try {
        const input = page.locator("#task-input");
        await input.fill("属于A的补充");
        await page
          .getByRole("button", { name: "发送补充", exact: true })
          .click();
        const sent = s.messages.find((message) => message.type === "steer");
        await input.fill("A的新草稿");
        await page.locator('.session-item[data-task-id="B"]').click();
        await page.waitForTimeout(60);
        await input.fill("B的新草稿");
        emit(s, {
          type: "steer_ack",
          taskId: "A",
          requestId: sent.requestId,
          state: "queued",
        });
        s.current.events.push({
          type: "user",
          id: 40,
          taskId: "A",
          requestId: sent.requestId,
          state: "queued",
          text: sent.task,
        });
        s.snapshot();
        await page.waitForTimeout(60);
        assert.match(await page.locator("#agent-title").innerText(), /B/);
        assert.equal(await input.inputValue(), "B的新草稿");
        assert.equal(await page.locator("[data-steer-request]").count(), 0);
        await page
          .getByRole("button", { name: "返回正在执行的会话", exact: true })
          .click();
        await page.waitForTimeout(60);
        assert.equal(await input.inputValue(), "A的新草稿");
        assert.equal(
          await page
            .locator('[data-steer-request="' + sent.requestId + '"]')
            .count(),
          1,
        );
      } finally {
        await context.close();
      }
    });
    test("steer reconnect confirms accepted event without resend or replacing a new draft", async () => {
      const { page, context, s } = await running();
      try {
        const input = page.locator("#task-input");
        await input.fill("断线前补充");
        await page
          .getByRole("button", { name: "发送补充", exact: true })
          .click();
        const sent = s.messages.find((m) => m.type === "steer");
        await input.fill("断线后的新草稿");
        s.current.events.push({
          type: "user",
          id: 32,
          taskId: "A",
          requestId: sent.requestId,
          state: "queued",
          text: sent.task,
        });
        s.sockets.at(-1).close();
        await page.waitForTimeout(800);
        assert.equal(s.messages.filter((m) => m.type === "steer").length, 1);
        assert.equal(await input.inputValue(), "断线后的新草稿");
        assert.match(
          await page
            .locator('[data-steer-request="' + sent.requestId + '"]')
            .innerText(),
          /等待应用|当前执行步骤结束后应用/,
        );
      } finally {
        await context.close();
      }
    });
    test("uncertain and rejected steer text remains recoverable without losing the new draft", async () => {
      const { page, context, s } = await running();
      try {
        const input = page.locator("#task-input");
        await input.fill("未确认补充");
        await page
          .getByRole("button", { name: "发送补充", exact: true })
          .click();
        const sent = s.messages.find((m) => m.type === "steer");
        await input.fill("必须保留的新草稿");
        s.sockets.at(-1).close();
        await page.waitForTimeout(800);
        const status = page.locator(
          '[data-steer-request="' + sent.requestId + '"]',
        );
        assert.match(await status.innerText(), /未确认|不确定/);
        assert.match(await status.innerText(), /未确认补充/);
        emit(s, {
          type: "error",
          taskId: "A",
          requestId: sent.requestId,
          text: "任务已停止，补充未接收",
        });
        await page.waitForTimeout(60);
        assert.match(await status.innerText(), /未接收|拒绝/);
        await status
          .getByRole("button", { name: "追加到草稿", exact: true })
          .click();
        assert.match(await input.inputValue(), /必须保留的新草稿/);
        assert.match(await input.inputValue(), /未确认补充/);
        assert.equal(s.messages.filter((m) => m.type === "steer").length, 1);
      } finally {
        await context.close();
      }
    });
    test("stopping prevents steer and deferred supplement stays visible for explicit continuation", async () => {
      const { page, context, s } = await running();
      try {
        const input = page.locator("#task-input");
        await input.fill("未应用补充");
        await page
          .getByRole("button", { name: "发送补充", exact: true })
          .click();
        const sent = s.messages.find((m) => m.type === "steer");
        emit(s, {
          type: "steer_ack",
          taskId: "A",
          requestId: sent.requestId,
          state: "queued",
        });
        s.current.events.push(
          {
            type: "user",
            id: 35,
            taskId: "A",
            requestId: sent.requestId,
            state: "queued",
            text: sent.task,
          },
          {
            type: "activity",
            id: 36,
            taskId: "A",
            requestId: sent.requestId,
            state: "steer_deferred",
            text: "补充尚未应用，手动继续此任务后处理。",
          },
        );
        s.current.phase = "stopping";
        s.state = "stopping";
        s.snapshot();
        await input.fill("不能发送");
        assert.equal(
          await page
            .getByRole("button", { name: "发送补充", exact: true })
            .isDisabled(),
          true,
        );
        s.current.phase = "finished";
        s.current.canContinue = true;
        s.state = "idle";
        s.snapshot();
        await page.waitForTimeout(80);
        assert.match(
          await page
            .locator('[data-steer-request="' + sent.requestId + '"]')
            .innerText(),
          /尚未应用|继续/,
        );
        assert.equal(s.messages.filter((m) => m.type === "start").length, 0);
      } finally {
        await context.close();
      }
    });
    await runTests(
      tests,
      (passed, total) => `${passed}/${total} checks passed`,
    );
  } finally {
    await browser?.close();
    vite.stop();
  }
})();
