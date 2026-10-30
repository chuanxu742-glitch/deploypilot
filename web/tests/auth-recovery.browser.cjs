const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const path = require("node:path");
const { spawn } = require("node:child_process");

(async () => {
  const root = path.resolve(__dirname, "../..");
  const port = process.env.DP_AUTH_PREVIEW_PORT || "4178";
  const preview = spawn(
    process.execPath,
    [
      path.join(
        path.dirname(require.resolve("vite/package.json")),
        "bin/vite.js",
      ),
      "preview",
      "--host",
      "127.0.0.1",
      "--port",
      port,
      "--strictPort",
    ],
    { cwd: path.join(root, "web"), stdio: "ignore" },
  );
  let browser;

  async function startApp(options = {}) {
    const context = await browser.newContext();
    if (options.cookie) {
      await context.addCookies([
        { name: "session", value: "valid", url: `http://127.0.0.1:${port}/` },
      ]);
    }
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    const state = {
      sessionReads: 0,
      modelListStatus: options.modelListStatus ?? 200,
      pairPosts: 0,
      agentConnections: 0,
      agentSockets: [],
      agentCloseCodes: [],
      failFirstAgentSocket: options.failFirstAgentSocket ?? false,
    };
    await page.route("**/api/**", async (route) => {
      const request = route.request();
      const endpoint = new URL(request.url()).pathname;
      if (endpoint === "/api/session") {
        state.sessionReads += 1;
        const mode =
          state.sessionReads === 1
            ? (options.initialSessionMode ?? "valid")
            : (options.disconnectSessionMode ?? "valid");
        if (mode === "network") {
          await route.abort("failed");
          return;
        }
        if (
          mode === "valid" &&
          options.cookie &&
          !request.headers().cookie?.includes("session=valid")
        ) {
          await route.fulfill({ status: 401, body: "unauthorized" });
          return;
        }
        const status = mode === "valid" ? 200 : Number(mode);
        await route.fulfill({
          status,
          json:
            status === 200 ? { ok: true } : { error: "session check failed" },
        });
        return;
      }
      if (endpoint === "/api/pair") {
        state.pairPosts += 1;
        await route.fulfill({ status: 401, body: "invalid token" });
        return;
      }
      if (endpoint === "/api/llm/models") {
        await route.fulfill({
          status: state.modelListStatus,
          json:
            state.modelListStatus === 200
              ? { models: ["test-model"] }
              : { error: "HTTP 401" },
        });
        return;
      }
      if (endpoint === "/api/llm/status") {
        await route.fulfill({ json: { hasKey: true, model: "test-model" } });
        return;
      }
      if (endpoint === "/api/tasks") {
        await route.fulfill({ json: [] });
        return;
      }
      if (endpoint.startsWith("/api/tasks/")) {
        await route.fulfill({ status: 404, body: "not found" });
        return;
      }
      if (endpoint === "/api/machine") {
        await route.fulfill({
          json: {
            hostname: "node",
            platform: "linux",
            username: "test",
            cwd: "/",
            version: "test",
          },
        });
        return;
      }
      if (endpoint === "/api/llm/config") {
        await route.fulfill({ json: { hasKey: true, model: "test-model" } });
        return;
      }
      await route.fulfill({ json: {} });
    });
    await page.routeWebSocket("**/ws/term", (socket) => {
      socket.send(JSON.stringify({ type: "agent_state", state: "idle" }));
      socket.onMessage(() => {});
    });
    await page.routeWebSocket("**/ws/agent", (socket) => {
      state.agentConnections += 1;
      state.agentSockets.push(socket);
      socket.send(
        JSON.stringify({
          type: "snapshot",
          state: "idle",
          task: null,
          tasks: [],
        }),
      );
      if (state.failFirstAgentSocket && state.agentConnections === 1) {
        void socket.close({
          code: 1011,
          reason: "simulated WebSocket connection failure",
        });
        return;
      }
      socket.onMessage(() => {});
    });
    await page.goto(`http://127.0.0.1:${port}/${options.fragment ?? ""}`);
    return { page, context, state };
  }

  const cases = [];
  const test = (name, run) => cases.push([name, run]);

  test("paired model-list 401 returns to pairing without a 4401 WebSocket close", async () => {
    const { page, context, state } = await startApp({ modelListStatus: 401 });
    await page.locator("#task-input").waitFor({ state: "visible" });
    await page.getByRole("button", { name: "模型设置" }).click();
    await page.getByRole("button", { name: "获取列表" }).click();
    await page.getByRole("heading", { name: "连接目标机器" }).waitFor();
    assert.equal(state.agentConnections, 1);
    assert.equal(state.pairPosts, 0);
    assert.equal(state.sessionReads, 1);
    await context.close();
  });

  test("ordinary WebSocket failure plus session 401 returns to pairing and stops retries", async () => {
    const { page, context, state } = await startApp({
      disconnectSessionMode: 401,
      failFirstAgentSocket: true,
    });
    await page.getByRole("heading", { name: "连接目标机器" }).waitFor();
    await page.waitForTimeout(600);
    assert.equal(state.agentConnections, 1);
    assert.equal(state.pairPosts, 0);
    assert.ok(
      state.sessionReads >= 2,
      "initial authentication and disconnect probe should check session",
    );
    await context.close();
  });

  for (const mode of ["network", 503]) {
    test(`disconnect session probe ${mode} does not log out and allows retry`, async () => {
      const { page, context, state } = await startApp({
        disconnectSessionMode: mode,
        failFirstAgentSocket: true,
      });
      await page.locator("#task-input").waitFor({ state: "visible" });
      await page.waitForFunction(
        () => document.querySelectorAll("#agent-panel").length === 1,
      );
      await page.waitForTimeout(600);
      assert.ok(state.sessionReads >= 2);
      assert.ok(
        state.agentConnections >= 2,
        `expected retry after probe result ${mode}`,
      );
      assert.equal(
        await page.getByRole("heading", { name: "连接目标机器" }).count(),
        0,
      );
      assert.equal(state.pairPosts, 0);
      await context.close();
    });
  }

  test("a valid cookie takes precedence over a reused pairing token", async () => {
    const { page, context, state } = await startApp({
      cookie: true,
      fragment: "#t=used-token",
    });
    await page.locator("#task-input").waitFor({ state: "visible" });
    assert.equal(state.pairPosts, 0);
    await page.waitForFunction(() => window.location.hash === "");
    assert.equal(state.sessionReads, 1);
    await context.close();
  });

  try {
    let ready = false;
    for (let attempt = 0; attempt < 100; attempt++) {
      if (preview.exitCode !== null)
        throw new Error(
          `Preview exited with code ${preview.exitCode}; port ${port} must be available.`,
        );
      try {
        if ((await fetch(`http://127.0.0.1:${port}`)).ok) {
          ready = true;
          break;
        }
      } catch {}
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    if (!ready) throw new Error(`Preview server did not start on ${port}`);
    browser = await chromium.launch({ channel: "chrome", headless: true });
    let failures = 0;
    for (const [name, run] of cases) {
      try {
        await run();
        console.log(`PASS ${name}`);
      } catch (error) {
        failures += 1;
        console.error(`FAIL ${name}: ${error.stack || error}`);
      }
    }
    if (failures) process.exitCode = 1;
    else console.log(`${cases.length} auth recovery browser checks passed`);
  } finally {
    await browser?.close();
    preview.kill();
  }
})();
