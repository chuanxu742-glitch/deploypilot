const { chromium } = require("playwright");
const assert = require("node:assert/strict");

(async () => {
  const url = process.env.DP_TEST_URL;
  const token = process.env.DP_TEST_TOKEN;
  assert.ok(url && token, "run through TestWaitForFrontendBrowserIntegration");
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  try {
    const context = await browser.newContext();
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const originalLink = `${url}/#t=${token}`;
    await page.goto(originalLink);
    await page.locator("#task-input").fill("等待本地服务就绪");
    await page.getByRole("button", { name: "执行任务", exact: true }).click();
    await page.getByText("等待条件", { exact: true }).waitFor();
    await page.locator(".action-progress").waitFor();
    assert.match(await page.locator(".action-progress").innerText(), /等待|秒/);
    // No authenticated request or model event is sent during this period.
    // The running task itself must preserve its owner past the 4s idle limit.
    await page.waitForTimeout(5500);
    await page.reload();
    await page.getByRole("button", { name: "停止任务", exact: true }).waitFor();
    await page.locator(".action-progress").waitFor();
    assert.equal(await (await fetch(`${url}/test/count`)).text(), "1");
    await fetch(`${url}/test/ready`, { method: "POST" });
    await page
      .locator(".final-card")
      .filter({ hasText: "等待验证完成" })
      .waitFor();
    const finishedWait = page
      .locator(".action")
      .filter({ hasText: "等待条件" })
      .first();
    if ((await finishedWait.getAttribute("open")) === null)
      await finishedWait.locator("summary").click();
    assert.match(await finishedWait.innerText(), /HTTP 204/);
    await fetch(`${url}/test/reset`, { method: "POST" });
    await page.locator("#task-input").fill("停止这次等待");
    await page.getByRole("button", { name: "执行任务", exact: true }).click();
    await page.getByRole("button", { name: "停止任务", exact: true }).waitFor();
    await page.waitForFunction(
      async () => (await fetch("/test/count").then((r) => r.text())) === "3",
    );
    await page.locator(".action-progress").waitFor();
    await page.getByRole("button", { name: "停止任务", exact: true }).click();
    await page.getByRole("button", { name: "执行任务", exact: true }).waitFor();
    await page.locator("#task-input").fill("可继续");
    // Controls are released only after the backend cleanup acknowledgement.
    await page
      .getByRole("button", { name: "执行任务", exact: true })
      .click({ trial: true });
    assert.equal(
      await page
        .getByRole("button", { name: "执行任务", exact: true })
        .isEnabled(),
      true,
    );
    await page.goto(originalLink);
    await page.locator("#task-input").waitFor();
    assert.equal(await page.locator(".gate-card").count(), 0);
    const other = await browser.newContext();
    const otherPage = await other.newPage();
    await otherPage.goto(originalLink);
    await otherPage.locator(".gate-card").waitFor();
    assert.equal(await otherPage.locator("#task-input").count(), 0);
    await other.close();
    assert.deepEqual(errors, []);
    console.log(
      "PASS real wait UI: silent task survives idle boundary, refresh preserves progress, actual HTTP result shown, stop releases controls, same-browser link reopens, consumed token rejected in new context",
    );
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
