const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

(async () => {
  const url = process.env.DP_TEST_URL;
  const token = process.env.DP_TEST_TOKEN;
  assert.ok(url && token, "run through TestConversationFrontendBrowserIntegration");
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  try {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 960 },
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));

    const assertBrandMarkLoaded = async () => {
      const mark = page.locator("img.brand-mark");
      await mark.waitFor();
      assert.equal(
        await mark.evaluate((image) => image.complete && image.naturalWidth > 0),
        true,
        "brand mark image must load",
      );
    };
    const getTask = (title) =>
      page.evaluate(async (wantedTitle) => {
        const summaries = await fetch("/api/tasks").then((response) =>
          response.json(),
        );
        const summary = summaries.find((item) => item.title === wantedTitle);
        return summary
          ? fetch(`/api/tasks/${encodeURIComponent(summary.id)}`).then(
              (response) => response.json(),
            )
          : null;
      }, title);

    await page.goto(`${url}/#t=${token}`);
    await page.locator("#task-input").waitFor();
    await assertBrandMarkLoaded();

    // A plain greeting is an ordinary assistant message, not an unsuccessful
    // deployment result, and it must take exactly one model request.
    await page.locator("#task-input").fill("hi");
    await page.getByRole("button", { name: "执行任务", exact: true }).click();
    await page
      .locator(".assistant-message")
      .filter({ hasText: "你好！我可以帮你整理部署步骤。" })
      .waitFor();
    await page.waitForFunction(async () => {
      const tasks = await fetch("/api/tasks").then((response) =>
        response.json(),
      );
      return tasks.some((task) => task.title === "hi" && task.phase === "finished");
    });
    await page
      .getByRole("button", { name: "停止任务", exact: true })
      .waitFor({ state: "detached" });
    assert.equal(await page.locator(".final-card").count(), 0);
    assert.equal(await page.locator(".tl-error").count(), 0);
    assert.equal(await (await fetch(`${url}/test/provider-count`)).text(), "1");

    // Start a deployment conversation; the provider asks for concrete input.
    await page.getByRole("button", { name: "新建任务", exact: true }).click();
    await page.locator("#task-input").fill("部署服务");
    await page.getByRole("button", { name: "执行任务", exact: true }).click();
    const needsInput = page
      .locator(".tl-final")
      .filter({ hasText: "等待补充信息" });
    await needsInput.waitFor();
    assert.match(await needsInput.innerText(), /目标服务器地址和登录方式/);
    assert.equal(await page.locator(".tl-error").count(), 0);
    assert.equal(await needsInput.locator(".final-card").count(), 1);
    await page.waitForFunction(async () => {
      const summaries = await fetch("/api/tasks").then((response) =>
        response.json(),
      );
      const summary = summaries.find((task) => task.title === "部署服务");
      if (!summary || summary.phase !== "finished") return false;
      const record = await fetch(`/api/tasks/${summary.id}`).then((response) =>
        response.json(),
      );
      return record.events.some(
        (event) => event.type === "final" && event.state === "needs_input",
      );
    });
    assert.equal(await (await fetch(`${url}/test/provider-count`)).text(), "2");
    await page
      .getByRole("button", { name: "停止任务", exact: true })
      .waitFor({ state: "detached" });

    const pending = await getTask("部署服务");
    assert.ok(pending);
    await page.reload();
    await page.locator("#task-input").waitFor();
    await assertBrandMarkLoaded();
    await page
      .locator(".tl-final")
      .filter({ hasText: "等待补充信息" })
      .waitFor();
    assert.equal(
      await page.locator("#task-input").getAttribute("placeholder"),
      "继续当前任务（点「新建任务」开始新会话）",
      "needs_input conversation must remain resumable after reload",
    );
    const restored = await getTask("部署服务");
    assert.equal(restored.id, pending.id);

    // Continue the same task. A normal finish(false) remains a red failure.
    const followup = "目标服务器地址是 192.0.2.10；使用测试帐号部署。";
    await page.locator("#task-input").fill(followup);
    await page.getByRole("button", { name: "执行任务", exact: true }).click();
    await page
      .locator(".tl-final.tl-error")
      .filter({ hasText: "模拟部署失败：测试服务器无法访问。" })
      .waitFor();
    await page.waitForFunction(async ({ id, oldRequestId, text }) => {
      const record = await fetch(`/api/tasks/${id}`).then((response) =>
        response.json(),
      );
      return (
        record.phase === "finished" &&
        record.requestId !== oldRequestId &&
        record.events.some((event) => event.type === "user" && event.text === text) &&
        record.events.some(
          (event) =>
            event.type === "final" &&
            event.success === false &&
            event.state !== "needs_input",
        )
      );
    }, { id: restored.id, oldRequestId: restored.requestId, text: followup });
    await page
      .getByRole("button", { name: "停止任务", exact: true })
      .waitFor({ state: "detached" });
    assert.equal(
      await page.locator(".final-card").filter({ hasText: "任务未完成" }).count(),
      1,
    );
    assert.equal(await (await fetch(`${url}/test/provider-count`)).text(), "3");
    await page.locator("#task-input").fill("检查任务已释放");
    assert.equal(
      await page.getByRole("button", { name: "执行任务", exact: true }).isEnabled(),
      true,
    );
    assert.deepEqual(errors, []);

    const root = path.resolve(__dirname, "../..");
    const screenshotDir = path.join(root, ".probe-tmp", "ui-screenshots");
    fs.mkdirSync(screenshotDir, { recursive: true });
    await page.screenshot({
      path: path.join(screenshotDir, "conversation-desktop.png"),
      fullPage: true,
      animations: "disabled",
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: path.join(screenshotDir, "conversation-mobile.png"),
      fullPage: true,
      animations: "disabled",
    });

    console.log(
      "PASS conversation Go HTTP/WS + Chrome: one-call greeting, needs-input card survives reload and continues, real failure stays red, controls release, brand mark loads",
    );
    console.log(`Screenshots: ${screenshotDir}`);
    await context.close();
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
