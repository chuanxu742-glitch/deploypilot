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
  const vite = await startVite({ defaultPort: "4315" });
  let browser;
  try {
    browser = await chromium.launch({ channel: "chrome", headless: true });
    const setup = createSetup(browser, vite.url, { coldStart: true });
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
    await runTests(
      tests,
      (passed, total) => `${passed}/${total} checks passed`,
    );
  } finally {
    await browser?.close();
    vite.stop();
  }
})();
