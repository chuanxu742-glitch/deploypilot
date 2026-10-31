// Shared scaffolding for the browser tests that run the web app against a
// mocked backend: a Vite server, task records, a page whose HTTP and
// WebSocket traffic is answered by an in-memory fake, and a test runner.
const path = require("node:path");
const { spawn } = require("node:child_process");

const webDir = path.resolve(__dirname, "../..");

/**
 * Starts Vite on a fixed port. DP_TASK_PREVIEW_PORT overrides the port and
 * DP_TASK_PREVIEW_DIR the build directory; `preview` serves the build, the
 * default is the dev server on the current source.
 */
async function startVite({ defaultPort, preview = false }) {
  const port = process.env.DP_TASK_PREVIEW_PORT || defaultPort;
  const args = [
    path.join(
      path.dirname(require.resolve("vite/package.json")),
      "bin/vite.js",
    ),
    ...(preview ? ["preview"] : ["--clearScreen", "false"]),
    "--host",
    "127.0.0.1",
    "--port",
    port,
    "--strictPort",
  ];
  if (process.env.DP_TASK_PREVIEW_DIR)
    args.push("--outDir", path.resolve(process.env.DP_TASK_PREVIEW_DIR));
  const child = spawn(process.execPath, args, { cwd: webDir, stdio: "ignore" });
  const url = `http://127.0.0.1:${port}`;
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(url)).ok) return { url, stop: () => child.kill() };
    } catch {}
    await new Promise((r) => setTimeout(r, 50));
  }
  child.kill();
  throw new Error(`Vite server did not start on ${port}`);
}

const record = (id, phase = "finished", events = []) => ({
  id,
  title: `任务 ${id}`,
  startedAt: Date.now() - 10000,
  endedAt: Date.now(),
  phase,
  events,
});

/**
 * Returns setup(records, options), which opens a page backed by a fake
 * server state `s`. Options of createSetup:
 * - viewport: default page size; null keeps Playwright's default.
 * - pruneClosedSockets: drop closed agent sockets from s.sockets.
 * - coldStart: allow 30 s for the first load (Vite dependency optimization).
 * Per call, options.viewport, options.statusWait and options.statusCode
 * change the page size and the /api/llm/status answer.
 */
function createSetup(
  browser,
  url,
  {
    viewport = { width: 1440, height: 960 },
    pruneClosedSockets = false,
    coldStart = false,
  } = {},
) {
  return async function setup(
    records = [record("latest"), record("A"), record("B")],
    options = {},
  ) {
    const size = options.viewport || viewport;
    const context = await browser.newContext(size ? { viewport: size } : {});
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
      if (pruneClosedSockets)
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
    const firstLoad = coldStart ? { timeout: 30000 } : {};
    await page.goto(url, firstLoad);
    await page.locator("#task-input").waitFor(firstLoad);
    if (records.length)
      await page
        .locator(".session-item")
        .first()
        .waitFor({ state: "attached" });
    await page.waitForTimeout(100);
    return { page, context, s };
  };
}

/**
 * Runs [name, fn] pairs in order, printing PASS/FAIL for each. summary gets
 * (passed, total) and returns the closing line, or null to print none.
 */
async function runTests(tests, summary) {
  let failures = 0;
  for (const [name, run] of tests) {
    try {
      await run();
      console.log(`PASS ${name}`);
    } catch (error) {
      failures++;
      console.error(`FAIL ${name}: ${error.stack || error}`);
    }
  }
  const line = summary(tests.length - failures, tests.length);
  if (line) console.log(line);
  if (failures) process.exitCode = 1;
}

module.exports = { startVite, record, createSetup, runTests };
