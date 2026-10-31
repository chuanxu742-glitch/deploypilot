const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const { mkdirSync, writeFileSync } = require("node:fs");
const { spawn } = require("node:child_process");
const path = require("node:path");

const root = path.resolve(__dirname, "../..");
const artifacts = process.env.DP_MARKDOWN_ARTIFACT_DIR
  ? path.resolve(root, process.env.DP_MARKDOWN_ARTIFACT_DIR)
  : path.join(root, ".probe-tmp/agent-ux-20261030");
mkdirSync(artifacts, { recursive: true });
const port = process.env.DP_MARKDOWN_PORT || "4372";
const results = [];
const exactCode = 'printf "%s\\n" "你好"\n\n  echo "保留缩进"\n';
const markdown = [
  "# 部署步骤",
  "",
  "这是**重要**内容，含*强调*、~~删除~~和 `inline()`。",
  "",
  "## 下一步",
  "",
  "- 列表甲",
  "- 列表乙",
  "",
  "1. 首项",
  "2. 次项",
  "",
  "- [x] 已验证",
  "- [ ] 待执行",
  "",
  "> 检查服务健康状态。",
  "",
  "| 名称 | 状态 |",
  "| --- | --- |",
  "| API | 正常 |",
  "",
  "```sh",
  exactCode,
  "```",
  "",
  "[参考](https://example.com/docs?a=1&b=2) https://example.com/plain",
  "",
  "[邮件](mailto:ops@example.com) [相对](../reference)",
].join("\n");
let browser;
const viteArgs = [path.join(root, "web/node_modules/vite/bin/vite.js")];
if (process.env.DP_MARKDOWN_DIST)
  viteArgs.push(
    "preview",
    "--outDir",
    path.resolve(process.env.DP_MARKDOWN_DIST),
  );
viteArgs.push(
  "--host",
  "127.0.0.1",
  "--port",
  port,
  "--strictPort",
  "--clearScreen",
  "false",
);
const vite = spawn(process.execPath, viteArgs, {
  cwd: path.join(root, "web"),
  stdio: "ignore",
  windowsHide: true,
});

async function setup(text = markdown, options = {}) {
  const context = await browser.newContext({
    viewport: options.viewport || { width: 1440, height: 1000 },
  });
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  const errors = [],
    requests = [],
    sockets = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("https://tracking.invalid/**", (route) => {
    requests.push(route.request().url());
    return route.abort();
  });
  await page.addInitScript(() => {
    window.__markdownXss = 0;
  });
  const record = {
    id: "markdown-case",
    title: "# 用户 **原文**",
    startedAt: Date.now() - 2000,
    phase: options.running ? "running" : "finished",
    events: [{ id: 1, taskId: "markdown-case", type: "thinking", text }],
  };
  if (!options.running)
    record.events.push(
      {
        id: 2,
        taskId: record.id,
        type: "tool_call",
        callId: "raw-command",
        tool: "run_command",
        command: "echo '**工具原文**'",
      },
      {
        id: 3,
        taskId: record.id,
        type: "output",
        callId: "raw-command",
        text: "# 原始工具输出\n**没有渲染**",
      },
      {
        id: 4,
        taskId: record.id,
        type: "tool_done",
        callId: "raw-command",
        tool: "run_command",
        success: true,
        exitCode: 0,
      },
      {
        id: 5,
        taskId: record.id,
        type: "final",
        text: "### 完成总结\n\n- 已核对 **服务**\n- [结果](https://example.com/result)",
        success: true,
      },
    );
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    const { events, ...summary } = record;
    const json =
      p === "/api/session"
        ? { status: "authenticated" }
        : p === "/api/machine"
          ? {
              hostname: "markdown-review",
              platform: "linux/amd64",
              username: "test",
              cwd: "/tmp/test",
              version: "test",
            }
          : p === "/api/llm/status"
            ? { hasKey: true, model: "local-fixture" }
            : p === "/api/tasks"
              ? [summary]
              : p.startsWith("/api/tasks/")
                ? record
                : {};
    await route.fulfill({ json });
  });
  await page.routeWebSocket("**/ws/term", (socket) =>
    setTimeout(() => {
      socket.send(JSON.stringify({ type: "terminal_reset" }));
      socket.send(
        JSON.stringify({
          type: "agent_state",
          state: options.running ? "running" : "idle",
        }),
      );
    }, 20),
  );
  await page.routeWebSocket("**/ws/agent", (socket) => {
    sockets.push(socket);
    setTimeout(
      () =>
        socket.send(
          JSON.stringify({
            type: "snapshot",
            state: options.running ? "running" : "idle",
            task: record,
            tasks: [record],
            continuable: options.running ? undefined : record.id,
          }),
        ),
      40,
    );
  });
  await page.goto(`http://127.0.0.1:${port}`, {
    waitUntil: "domcontentloaded",
    timeout: 30000,
  });
  await page.locator(".assistant-message").waitFor({ timeout: 30000 });
  return { page, context, errors, requests, sockets, record };
}

async function check(name, run) {
  try {
    await run();
    results.push({ name, passed: true });
    console.log(`PASS ${name}`);
  } catch (e) {
    results.push({ name, passed: false, error: e.stack || String(e) });
    console.error(`FAIL ${name}: ${e.message}`);
  }
}

(async () => {
  try {
    for (let i = 0; i < 200; i++) {
      if (vite.exitCode !== null)
        throw new Error("Markdown test Vite server exited");
      try {
        if ((await fetch(`http://127.0.0.1:${port}`)).ok) break;
      } catch {}
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    browser = await chromium.launch({ channel: "chrome", headless: true });
    await check(
      "assistant and final render CommonMark/GFM; operator and tools stay literal",
      async () => {
        const s = await setup();
        try {
          const body = s.page.locator(".assistant-message");
          assert.equal(
            await body.locator("h1").count(),
            1,
            "assistant heading must be a real heading",
          );
          assert.equal(await body.locator("h2").count(), 1);
          assert.equal(await body.locator("blockquote").count(), 1);
          assert.equal(
            await body.locator("table tbody td").first().innerText(),
            "API",
          );
          assert.equal(
            await body.locator('input[type="checkbox"]:checked').count(),
            1,
          );
          assert.equal(
            await body.locator('input[type="checkbox"]:disabled').count(),
            2,
          );
          assert.equal(await body.locator("ol > li").count(), 2);
          assert.equal(await body.locator("del").innerText(), "删除");
          assert.equal(await body.locator("em").innerText(), "强调");
          assert.equal(await body.locator("p code").innerText(), "inline()");
          assert.equal(
            await s.page.locator(".final-card h3").innerText(),
            "完成总结",
          );
          assert.equal(
            await s.page.locator(".task-text").innerText(),
            "# 用户 **原文**",
          );
          assert.equal(
            await s.page.locator(".task-text strong, .task-text h1").count(),
            0,
          );
          await s.page.locator(".action summary").click();
          assert.match(
            await s.page.locator(".action-output pre").innerText(),
            /# 原始工具输出\n\*\*没有渲染\*\*/,
          );
          assert.deepEqual(s.errors, []);
        } finally {
          await s.context.close();
        }
      },
    );
    if (process.env.DP_MARKDOWN_RED_ONLY === "1") return;

    await check(
      "code language and clipboard preserve exact code including blank lines",
      async () => {
        const s = await setup();
        try {
          const body = s.page.locator(".assistant-message");
          assert.equal(await body.locator("pre code").textContent(), exactCode);
          assert.match(
            await body.locator("pre").locator("..").innerText(),
            /sh/,
          );
          await s.page.evaluate(() => {
            const write = navigator.clipboard.writeText.bind(
              navigator.clipboard,
            );
            navigator.clipboard.writeText = async (text) => {
              window.__copiedMarkdownCode = text;
              return write(text);
            };
          });
          await body
            .getByRole("button", { name: "复制代码", exact: true })
            .click();
          assert.equal(
            await s.page.evaluate(() => window.__copiedMarkdownCode),
            exactCode,
          );
          // Windows' native clipboard exposes CRLF even when writeText receives LF.
          assert.equal(
            (
              await s.page.evaluate(() => navigator.clipboard.readText())
            ).replace(/\r\n/g, "\n"),
            exactCode,
          );
          await s.page.evaluate(() =>
            Object.defineProperty(navigator, "clipboard", {
              configurable: true,
              value: {
                writeText: async () => {
                  throw new Error("denied");
                },
              },
            }),
          );
          await body
            .getByRole("button", { name: "复制代码", exact: true })
            .click();
          assert.match(await body.getByRole("status").innerText(), /复制失败/);
        } finally {
          await s.context.close();
        }
      },
    );

    await check(
      "links are useful and hostile protocols/HTML/images cannot execute or request",
      async () => {
        const unsafe = [
          markdown,
          "",
          "<script>window.__markdownXss=1</script>",
          "",
          '<img src="https://tracking.invalid/raw.png" onerror="window.__markdownXss=2">',
          "",
          "![外图](https://tracking.invalid/image.png)",
          "",
          "[危险](javascript:alert%281%29) [数据](data:text/html,bad) [文件](file:///C:/Windows) [混淆](javascript&#58;alert%281%29)",
        ].join("\n");
        const s = await setup(unsafe);
        try {
          const body = s.page.locator(".assistant-message");
          const safe = body.getByRole("link", { name: "参考", exact: true });
          assert.equal(
            await safe.getAttribute("href"),
            "https://example.com/docs?a=1&b=2",
          );
          assert.equal(await safe.getAttribute("target"), "_blank");
          assert.match(await safe.getAttribute("rel"), /noopener/);
          assert.equal(
            await body
              .getByRole("link", {
                name: "https://example.com/plain",
                exact: true,
              })
              .count(),
            1,
          );
          assert.equal(
            await body
              .getByRole("link", { name: "邮件", exact: true })
              .getAttribute("href"),
            "mailto:ops@example.com",
          );
          assert.equal(
            await body
              .getByRole("link", { name: "相对", exact: true })
              .getAttribute("href"),
            "../reference",
          );
          const hrefs = await body
            .locator("a")
            .evaluateAll((xs) => xs.map((x) => x.getAttribute("href")));
          assert.ok(
            hrefs.every(
              (href) =>
                !/^(?:javascript|data|file|vbscript):/i.test(href || ""),
            ),
            hrefs.join("\n"),
          );
          assert.equal(
            await body.locator("script, img, iframe, svg, object").count(),
            0,
          );
          assert.equal(await s.page.evaluate(() => window.__markdownXss), 0);
          await s.page.waitForTimeout(120);
          assert.deepEqual(
            s.requests,
            [],
            "an external Markdown image must not request the network",
          );
          assert.deepEqual(s.errors, []);
        } finally {
          await s.context.close();
        }
      },
    );

    await check(
      "unfinished streaming fences keep every character and recover when closed",
      async () => {
        const initial = '# 流式回答\n\n```python\nprint("未闭合")  ';
        const s = await setup(initial, { running: true });
        try {
          const body = s.page.locator(".assistant-message");
          assert.equal(
            await body.locator("pre code").textContent(),
            'print("未闭合")  ',
          );
          s.sockets.at(-1).send(
            JSON.stringify({
              type: "thinking",
              taskId: s.record.id,
              id: 2,
              text: '\nprint("后续")\n```\n\n**继续**',
            }),
          );
          await body.locator("strong").waitFor();
          assert.equal(
            await body.locator("pre code").textContent(),
            'print("未闭合")  \nprint("后续")',
          );
          assert.equal(await body.locator("strong").innerText(), "继续");
          assert.deepEqual(s.errors, []);
        } finally {
          await s.context.close();
        }
      },
    );

    await check(
      "mobile code/table scroll locally and all three Radix themes remain legible",
      async () => {
        const wide =
          markdown +
          "\n\n```text\n" +
          "长行".repeat(160) +
          "\n```\n\n| 一 | 二 | 三 |\n|---|---|---|\n| " +
          ["x".repeat(150), "y".repeat(150), "z".repeat(150)].join(" | ") +
          " |";
        const s = await setup(wide, { viewport: { width: 390, height: 844 } });
        try {
          const palettes = [];
          for (const [theme, label] of [
            ["day", "日间"],
            ["night", "夜间"],
            ["comfort", "护眼"],
          ]) {
            await s.page
              .getByRole("button", { name: "外观设置", exact: true })
              .click();
            await s.page
              .getByRole("button", { name: label, exact: true })
              .click();
            await s.page
              .getByRole("button", { name: "关闭外观设置", exact: true })
              .click();
            const sizes = await s.page
              .locator(".assistant-message")
              .evaluate((el) => ({
                pageWidth: document.documentElement.scrollWidth,
                viewport: innerWidth,
                heading: getComputedStyle(el.querySelector("h1")).fontSize,
                body: getComputedStyle(el.querySelector("p")).fontSize,
                code: getComputedStyle(el.querySelector("pre code")).fontSize,
                color: getComputedStyle(el).color,
                codeBackground: getComputedStyle(el.querySelector("pre"))
                  .backgroundColor,
                overflow: [...el.querySelectorAll("pre")].some(
                  (x) => x.scrollWidth > x.clientWidth,
                ),
                tableOverflow: [
                  ...el.querySelectorAll(".markdown-table-scroll"),
                ].some((x) => x.scrollWidth > x.clientWidth),
              }));
            assert.ok(
              sizes.pageWidth <= sizes.viewport + 1,
              `${theme}: page overflow ${JSON.stringify(sizes)}`,
            );
            assert.equal(sizes.heading, "24px");
            assert.equal(sizes.body, "14px");
            assert.equal(sizes.code, "14px");
            assert.ok(
              sizes.overflow,
              "long code should scroll inside its block",
            );
            assert.ok(
              sizes.tableOverflow,
              "wide tables should scroll inside their region",
            );
            palettes.push(sizes.codeBackground);
            await s.page.screenshot({
              path: path.join(artifacts, `markdown-${theme}-mobile.png`),
              fullPage: true,
            });
          }
          assert.equal(
            new Set(palettes).size,
            3,
            "code backgrounds must follow the three themes",
          );
          assert.deepEqual(s.errors, []);
        } finally {
          await s.context.close();
        }
      },
    );
  } finally {
    writeFileSync(
      path.join(
        artifacts,
        `markdown-${process.env.DP_MARKDOWN_RED_ONLY ? "red" : "green"}-results.json`,
      ),
      JSON.stringify(results, null, 2),
    );
    if (browser) await browser.close();
    vite.kill();
    if (results.some((x) => !x.passed)) process.exitCode = 1;
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
