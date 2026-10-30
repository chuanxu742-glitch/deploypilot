const { chromium } = require('playwright');
const assert = require('node:assert/strict');

(async () => {
  const url = process.env.DP_TEST_URL;
  const token = process.env.DP_TEST_TOKEN;
  assert.ok(url && token, 'run through TestFrontendBrowserIntegration');
  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 960 } });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    let dropFirstReceipt = true;
    let starts = 0;
    let connections = 0;
    await page.routeWebSocket('**/ws/agent', socket => {
      connections++;
      const upstream = socket.connectToServer();
      let droppedConnection = false;
      socket.onMessage(data => {
        if (JSON.parse(String(data)).type === 'start') starts++;
        upstream.send(data);
      });
      upstream.onMessage(async data => {
        if (droppedConnection) return;
        const value = JSON.parse(String(data));
        if (dropFirstReceipt && value.type === 'snapshot' && value.task?.requestId) {
          dropFirstReceipt = false;
          droppedConnection = true;
          await upstream.close();
          await socket.close();
          return;
        }
        socket.send(data);
      });
    });
    await page.goto(`${url}/#t=${token}`);
    await page.locator('#task-input').waitFor();
    await page.locator('#task-input').fill('真实后端启动确认');
    await page.getByRole('button', { name: '执行任务', exact: true }).click();
    await page.waitForFunction(async () => {
      const tasks = await fetch('/api/tasks').then(r => r.json());
      return tasks.length === 1 && !!tasks[0].requestId && tasks[0].phase === 'running';
    });
    const first = await page.evaluate(async () => (await fetch('/api/tasks').then(r => r.json()))[0]);
    await page.waitForFunction(id => document.querySelector('.task-history-select')?.value === id, first.id);
    assert.ok(connections >= 2, 'lost acceptance receipt must force a reconnect');
    assert.equal(starts, 1, 'reconnect must not repeat a transmitted start');
    await page.reload();
    await page.getByRole('button', { name: '停止任务', exact: true }).waitFor();
    assert.equal(await (await fetch(`${url}/test/provider-count`)).text(), '1');
    await fetch(`${url}/test/release`, { method: 'POST' });
    await page.getByText('本地联调完成', { exact: true }).waitFor();
    await page.locator('#task-input').fill('同一任务补充消息');
    await page.getByRole('button', { name: '执行任务', exact: true }).click();
    await page.getByText('同一任务补充消息', { exact: true }).waitFor();
    await page.waitForFunction(async oldRequest => {
      const tasks = await fetch('/api/tasks').then(r => r.json());
      return tasks.length === 1 && tasks[0].phase === 'finished' && tasks[0].requestId !== oldRequest;
    }, first.requestId);
    const continued = await page.evaluate(async () => (await fetch('/api/tasks').then(r => r.json()))[0]);
    assert.equal(continued.id, first.id);
    assert.notEqual(continued.requestId, first.requestId);
    await page.goto(`${url}/?revisit=1#t=${token}`);
    await page.locator('#task-input').waitFor();
    assert.equal(await page.locator('.gate-card').count(), 0, 'used token must not block a valid cookie');
    assert.equal(await (await fetch(`${url}/test/provider-count`)).text(), '2');
    assert.equal(starts, 2, 'one new submission and one explicit followup only');
    // Revoke only this ephemeral test session. A protected HTTP request must
    // return the actual application to its pairing gate.
    await fetch(`${url}/test/expire`, { method: 'POST' });
    await page.evaluate(() => fetch('/api/llm/status').catch(() => {}));
    await page.getByRole('heading', { name: '连接目标机器', exact: true }).waitFor();
    assert.deepEqual(errors, []);
    console.log('PASS real Go HTTP/WS + Chrome: lost acceptance receipt recovers without replay, active reload, same-session followup, used link, expired session');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
