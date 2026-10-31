const { chromium } = require("playwright");
const path = require("node:path");
const { spawn } = require("node:child_process");

(async () => {
  const root = path.resolve(__dirname, "../..");
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
      "4176",
      "--strictPort",
    ],
    { cwd: path.join(root, "web"), stdio: "ignore" },
  );
  let browser;
  try {
    let ready = false;
    for (let attempt = 0; attempt < 100; attempt++) {
      try {
        if ((await fetch("http://127.0.0.1:4176")).ok) {
          ready = true;
          break;
        }
      } catch {}
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    if (!ready) throw new Error("Preview server did not start.");

    const now = Date.now();
    const taskId = "input-perf";
    const events = Array.from({ length: 200 }, (_, index) => ({
      id: index + 1,
      taskId,
      type: "status",
      text: `row-${index + 1} ${"x".repeat(2048)}`,
      timestamp: now + index,
    }));
    const summary = {
      id: taskId,
      title: "输入性能观测",
      startedAt: now,
      phase: "finished",
      endedAt: now + 200,
    };
    const record = { ...summary, events };

    browser = await chromium.launch({ channel: "chrome", headless: true });
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
    });
    await page.addInitScript(() => {
      window.__longTasks = [];
      window.__inputDurations = [];
      let inputStarted = 0;
      document.addEventListener(
        "input",
        (event) => {
          if (event.target?.id === "task-input")
            inputStarted = performance.now();
        },
        true,
      );
      document.addEventListener("input", (event) => {
        if (event.target?.id === "task-input")
          window.__inputDurations.push(performance.now() - inputStarted);
      });
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries())
          window.__longTasks.push(entry.duration);
      }).observe({ type: "longtask", buffered: true });
    });
    await page.route("**/api/**", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/api/tasks")
        return route.fulfill({ json: [summary] });
      if (url.pathname === `/api/tasks/${taskId}`)
        return route.fulfill({ json: record });
      const body = url.pathname.endsWith("/health")
        ? { status: "ok", version: "perf" }
        : url.pathname.endsWith("/machine")
          ? {
              hostname: "perf",
              platform: "linux/amd64",
              username: "perf",
              cwd: "/",
              version: "perf",
            }
          : url.pathname.endsWith("/models")
            ? { models: ["bailu-auto"] }
            : { hasKey: true, model: "bailu-auto" };
      return route.fulfill({ json: body });
    });
    await page.routeWebSocket("**/ws/term", (socket) => {
      socket.send(JSON.stringify({ type: "terminal_reset" }));
      socket.send(JSON.stringify({ type: "agent_state", state: "idle" }));
    });
    await page.routeWebSocket("**/ws/agent", (socket) => {
      socket.send(
        JSON.stringify({
          type: "snapshot",
          state: "idle",
          task: record,
          tasks: [summary],
        }),
      );
    });
    await page.goto("http://127.0.0.1:4176");
    const input = page.locator("#task-input");
    await input.waitFor();
    await page.waitForFunction(
      () => document.querySelectorAll(".tl-note").length >= 200,
    );
    await page.evaluate(() => {
      window.__longTasks = [];
      window.__inputDurations = [];
    });
    await input.click();
    const start = Date.now();
    await page.keyboard.type("x".repeat(100), { delay: 0 });
    const elapsedMs = Date.now() - start;
    const details = await page.evaluate(() => ({
      longTasks: window.__longTasks,
      inputDurations: window.__inputDurations,
      valueLength: document.querySelector("#task-input").value.length,
      rows: document.querySelectorAll(".tl-note").length,
    }));
    const sortedDurations = [...details.inputDurations].sort((a, b) => a - b);
    console.log(
      JSON.stringify({
        elapsedMs,
        msPerKey: elapsedMs / 100,
        inputEvents: sortedDurations.length,
        inputHandlerMs: {
          mean:
            sortedDurations.reduce((sum, value) => sum + value, 0) /
            Math.max(1, sortedDurations.length),
          p95: sortedDurations[Math.floor(sortedDurations.length * 0.95)] ?? 0,
          max: sortedDurations.at(-1) ?? 0,
        },
        longTasks: details.longTasks,
        valueLength: details.valueLength,
        rows: details.rows,
      }),
    );
  } finally {
    await browser?.close();
    preview.kill();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
