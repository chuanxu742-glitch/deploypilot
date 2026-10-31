// Real-model browser scenarios. Run only through TestAgentLiveBrowser, which
// serves the built app with the real Go server, terminal and model, and puts
// every command behind a guard. Findings are recorded instead of stopping at
// the first one, so a single run shows every problem.
const { chromium } = require("playwright");
const fs = require("node:fs");
const path = require("node:path");

const url = process.env.DP_TEST_URL;
const token = process.env.DP_TEST_TOKEN;
const work = process.env.DP_TEST_WORK;
const out = process.env.DP_TEST_OUT;
if (!url || !token || !work || !out) {
  console.error("run through TestAgentLiveBrowser");
  process.exit(2);
}
fs.mkdirSync(out, { recursive: true });
const only = (process.env.DP_LIVE_BROWSER_ONLY || "")
  .split(",")
  .filter(Boolean);
const policy =
  "注意：这台测试机只允许在该目录内创建、修改、删除文件；不要安装软件或修改系统配置；如需停止进程，只能用你自己启动的进程的数字 PID（例如 Stop-Process -Id 1234）。命令使用 Windows PowerShell，Python 命令是 python。";
const dir = (name) => path.join(work, name);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const results = [];
let scenario = "";
function check(name, ok, detail = "") {
  results.push({ scenario, name, ok: !!ok, detail: String(detail) });
  console.log(
    `${ok ? "ok  " : "FAIL"} [${scenario}] ${name}${detail ? ": " + detail : ""}`,
  );
  return !!ok;
}
function note(text) {
  console.log(`     [${scenario}] ${text}`);
}

const pageErrors = [];
const consoleErrors = [];
const badResponses = [];
function observe(page, label) {
  page.sent = [];
  page.received = [];
  page.on("pageerror", (e) => pageErrors.push(`${label}: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() === "error" && !/status of 401/.test(m.text()))
      consoleErrors.push(`${label}: ${m.text()}`);
  });
  page.on("response", (r) => {
    // The first visit probes the cookie before pairing; that 401 is expected.
    const firstProbe = r.status() === 401 && r.url().endsWith("/api/session");
    if (r.status() >= 400 && !r.url().includes("/test/") && !firstProbe)
      badResponses.push(`${label}: ${r.status()} ${r.url()}`);
  });
  page.on("websocket", (ws) => {
    if (!ws.url().includes("/ws/agent")) return;
    ws.on("framesent", (f) => {
      try {
        page.sent.push(JSON.parse(String(f.payload)));
      } catch {}
    });
    ws.on("framereceived", (f) => {
      try {
        page.received.push(JSON.parse(String(f.payload)));
      } catch {}
    });
  });
}

const api = (page, p) =>
  page.evaluate(async (p) => {
    const r = await fetch(p);
    return r.ok ? r.json() : { status: r.status };
  }, p);
const tasks = (page) => api(page, "/api/tasks");
async function shot(page, name) {
  await page
    .screenshot({ path: path.join(out, `${name}.png`), fullPage: false })
    .catch(() => {});
}
const input = (page) => page.locator("#task-input");
// A supplement is sent only by clicking, by design; tasks use Enter.
async function submit(page, text, { click = false } = {}) {
  await input(page).fill(text);
  const before = page.sent.length;
  if (click)
    await page.getByRole("button", { name: "发送补充", exact: true }).click();
  else await input(page).press("Enter");
  for (let i = 0; i < 50 && page.sent.length === before; i++) await sleep(100);
  return page.sent
    .slice(before)
    .find((m) => m.type === "start" || m.type === "steer");
}

// Waits until the server has the request running and returns its record.
async function waitAccepted(page, requestId, limit = 30000) {
  const end = Date.now() + limit;
  while (Date.now() < end) {
    const list = await tasks(page);
    const t =
      Array.isArray(list) && list.find((x) => x.requestId === requestId);
    if (t) return t;
    await sleep(500);
  }
  return null;
}

async function waitServerIdle(page, limit) {
  const end = Date.now() + limit;
  while (Date.now() < end) {
    const list = await tasks(page).catch(() => null);
    if (
      Array.isArray(list) &&
      !list.some((t) => t.phase === "running" || t.phase === "stopping")
    )
      return true;
    await sleep(2000);
  }
  return false;
}

async function waitFor(fn, limit, every = 500) {
  const end = Date.now() + limit;
  while (Date.now() < end) {
    try {
      const v = await fn();
      if (v) return v;
    } catch {}
    await sleep(every);
  }
  return null;
}

async function httpText(target, limit = 10000) {
  return waitFor(async () => {
    const r = await fetch(target, { signal: AbortSignal.timeout(2000) });
    return { status: r.status, text: (await r.text()).trim() };
  }, limit);
}

const lineCount = (file) => {
  try {
    return fs.readFileSync(file, "utf8").split(/\r?\n/).filter(Boolean).length;
  } catch {
    return -1;
  }
};

async function timelineState(page) {
  return page.evaluate(() => {
    const finals = [...document.querySelectorAll(".tl-final")];
    const last = finals.at(-1);
    // What the operator sees as the outcome: the last final card, or a plain
    // assistant reply that ends the turn (a neutral reply has no card).
    const rows = [...document.querySelectorAll(".timeline > li")];
    const lastRow = rows.at(-1);
    const ending = lastRow?.classList.contains("tl-final")
      ? "final"
      : lastRow?.classList.contains("assistant-message")
        ? "reply"
        : (lastRow?.className ?? "");
    return {
      ending,
      replies: document.querySelectorAll(".assistant-message").length,
      actions: document.querySelectorAll(".tl-action").length,
      // Turn outcomes: final cards plus replies that end a turn.
      finals:
        finals.length +
        [
          ...document.querySelectorAll(".timeline > li.assistant-message"),
        ].filter(
          (li) =>
            !li.nextElementSibling ||
            li.nextElementSibling.classList.contains("tl-task"),
        ).length,
      cards: finals.length,
      lastFinalTitle:
        ending === "reply"
          ? "任务结束(回复)"
          : (last?.querySelector("strong")?.textContent ?? ""),
      lastFinalText: (ending === "reply" ? lastRow : last)?.innerText ?? "",
      errors: [...document.querySelectorAll(".tl-error .tl-body")].map(
        (e) => e.textContent,
      ),
      users: [...document.querySelectorAll(".tl-task .task-text")].map(
        (e) => e.textContent,
      ),
      status:
        document.querySelector(".run-status [role=status]")?.textContent ?? "",
      stopVisible: !!document.querySelector('button[aria-label="停止任务"]'),
      inputDisabled: document.querySelector("#task-input")?.disabled ?? null,
      placeholder: document.querySelector("#task-input")?.placeholder ?? "",
      sessions: [...document.querySelectorAll(".session-item")].map((e) => ({
        id: e.dataset.taskId,
        current: e.getAttribute("aria-current") === "page",
        text: e.innerText.replace(/\s+/g, " "),
      })),
      termReadonly: document.querySelector(".term")?.dataset.readonly ?? null,
      termText: (document.querySelector(".xterm-rows")?.textContent ?? "")
        .length,
      overflowX: document.scrollingElement.scrollWidth - window.innerWidth,
    };
  });
}

async function layoutChecks(page, label) {
  const m = await page.evaluate(() => {
    const rect = (s) => {
      const e = document.querySelector(s);
      if (!e) return null;
      const r = e.getBoundingClientRect();
      return {
        w: Math.round(r.width),
        h: Math.round(r.height),
        x: Math.round(r.x),
        y: Math.round(r.y),
      };
    };
    // Any visible element wider than the viewport that is not inside a local scroller.
    const wide = [];
    for (const e of document.querySelectorAll("body *")) {
      const r = e.getBoundingClientRect();
      if (r.width === 0 || r.right <= window.innerWidth + 1) continue;
      let p = e.parentElement;
      let scrolled = false;
      while (p) {
        const s = getComputedStyle(p);
        if (/(auto|scroll|hidden)/.test(s.overflowX)) {
          scrolled = true;
          break;
        }
        p = p.parentElement;
      }
      if (!scrolled) wide.push(e.tagName + "." + e.className);
    }
    return {
      viewport: { w: window.innerWidth, h: window.innerHeight },
      agent: rect(".agent-panel, #agent-panel, .panel.agent"),
      scroll: rect(".agent-scroll"),
      dock: rect(".dock"),
      overflowX: document.scrollingElement.scrollWidth - window.innerWidth,
      wide: wide.slice(0, 5),
    };
  });
  note(`${label} layout ${JSON.stringify(m)}`);
  check(
    `${label}: no page-level horizontal overflow`,
    m.overflowX <= 1 && m.wide.length === 0,
    JSON.stringify(m.wide),
  );
  return m;
}

// The final card renders the model summary plus the server's raw evidence.
async function finalCardChecks(page, label) {
  const f = await page.evaluate(() => {
    const last = [...document.querySelectorAll(".timeline > li")].at(-1);
    const card = last?.classList.contains("tl-final")
      ? last.querySelector(".final-card")
      : last?.classList.contains("assistant-message")
        ? last
        : null;
    if (!card) return null;
    const md = card.querySelector(".agent-markdown");
    const html = md?.innerHTML ?? "";
    const idx = (card.innerText || "").indexOf("实际验证记录");
    return {
      kind: card === last ? "reply" : "final",
      title: card.querySelector(":scope > strong")?.textContent ?? "",
      headings: card.querySelectorAll("h1,h2,h3,h4,h5,h6").length,
      headingTexts: [...card.querySelectorAll("h1,h2,h3,h4,h5,h6")]
        .map((h) => h.textContent)
        .slice(0, 8),
      tables: card.querySelectorAll("table").length,
      codeBlocks: card.querySelectorAll(".markdown-code-block").length,
      hr: card.querySelectorAll("hr").length,
      evidenceAt: idx,
      evidenceText: idx >= 0 ? card.innerText.slice(idx, idx + 1200) : "",
      evidencePre: [...card.querySelectorAll("pre")].some((p) =>
        p.textContent.includes("exit="),
      ),
      htmlLen: html.length,
    };
  });
  if (!check(`${label}: outcome rendered`, !!f)) return null;
  note(`${label} final ${JSON.stringify({ ...f, evidenceText: undefined })}`);
  note(`${label} evidence as shown:\n${f.evidenceText}`);
  return f;
}

async function runScenario(name, fn) {
  if (only.length && !only.includes(name)) return;
  scenario = name;
  const started = Date.now();
  console.log(`\n=== ${name}`);
  try {
    await fn();
  } catch (e) {
    check("scenario completed without exception", false, e.stack || e.message);
  }
  note(`took ${Math.round((Date.now() - started) / 1000)}s`);
}

(async () => {
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  const desktop = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    permissions: ["clipboard-read", "clipboard-write"],
  });
  const page = await desktop.newPage();
  page.setDefaultTimeout(15000);
  observe(page, "desktop");
  const s1Port = 18931;
  let s1TaskId = "";

  await runScenario("pair_and_deploy_with_reload", async () => {
    await page.goto(`${url}/#t=${token}`);
    await input(page).waitFor();
    check(
      "pairing link opens the app",
      (await page.locator(".gate-card").count()) === 0,
    );
    check(
      "pairing token removed from address bar",
      !page.url().includes(token),
      page.url(),
    );
    await waitFor(async () => !(await input(page).isDisabled()), 10000);
    const text = `在目录 ${dir("s1")}（不存在就创建）用 Python 标准库写 server.py：监听 127.0.0.1:${s1Port}，GET / 返回纯文本 hello-ui。用 Start-Process 在后台启动，不要阻塞终端，然后验证。最后的总结请用 Markdown：一个三级标题、一个表格（列出文件、端口、PID）、一个 powershell 代码块写出停止命令。${policy}`;
    const start = await submit(page, text);
    check(
      "Enter sends a start frame",
      start?.type === "start",
      JSON.stringify(start ?? {}).slice(0, 200),
    );
    check("first task is a new session", start && !start.continueFrom);
    const rec = start && (await waitAccepted(page, start.requestId));
    check("server accepted the task", !!rec);
    s1TaskId = rec?.id ?? "";
    await page.locator(".tl-action").first().waitFor({ timeout: 180000 });
    check(
      "stop control visible while running",
      (await timelineState(page)).stopVisible,
    );
    const termHas = await waitFor(
      async () => (await timelineState(page)).termText > 0,
      30000,
    );
    check("terminal panel shows output while the agent works", !!termHas);
    // Wait for at least one completed action, then reload mid-run.
    await waitFor(
      () =>
        page
          .locator(
            ".tl-action .chip[data-tone=ok], .tl-action .chip[data-tone=bad]",
          )
          .count(),
      180000,
    );
    const before = await timelineState(page);
    await shot(page, "s1-running");
    const running = (await tasks(page)).some((t) => t.phase === "running");
    note(`reload while running=${running}, actions=${before.actions}`);
    const startsBefore = page.sent.filter((m) => m.type === "start").length;
    await page.reload();
    await input(page).waitFor();
    const after = await waitFor(async () => {
      const s = await timelineState(page);
      return s.actions >= before.actions ? s : null;
    }, 15000);
    check(
      "reload restores the running session's actions",
      !!after,
      `before=${before.actions} after=${after?.actions}`,
    );
    if (running) {
      const still = await tasks(page);
      if (still.some((t) => t.phase === "running"))
        check(
          "reload keeps the stop control",
          (await timelineState(page)).stopVisible,
        );
    }
    // A second tab sees the same session.
    const tab2 = await desktop.newPage();
    observe(tab2, "tab2");
    await tab2.goto(url);
    await input(tab2).waitFor();
    const tab2State = await waitFor(async () => {
      const s = await timelineState(tab2);
      return s.sessions.length ? s : null;
    }, 10000);
    check(
      "second tab lists the session",
      tab2State?.sessions.some((x) => x.id === s1TaskId),
      JSON.stringify(tab2State?.sessions),
    );
    check("idle wait", await waitServerIdle(page, 8 * 60000));
    const ui = await waitFor(async () => {
      const s = await timelineState(page);
      return s.finals > 0 && !s.stopVisible ? s : null;
    }, 15000);
    check(
      "UI shows the final result after the run",
      !!ui,
      JSON.stringify(await timelineState(page)).slice(0, 400),
    );
    check(
      "no duplicate start after reload",
      page.sent.filter((m) => m.type === "start").length === startsBefore,
      `${page.sent.filter((m) => m.type === "start").length} vs ${startsBefore}`,
    );
    const st = await timelineState(page);
    check(
      "final reports success",
      st.lastFinalTitle?.startsWith("任务结束"),
      st.lastFinalTitle,
    );
    const svc = await httpText(`http://127.0.0.1:${s1Port}/`);
    check(
      "service really answers hello-ui",
      svc?.text === "hello-ui",
      JSON.stringify(svc),
    );
    const f = await finalCardChecks(page, "s1");
    if (f) {
      // Whether the model writes a table or heading is its choice; when it
      // does, it must render. Raw evidence must always stay a literal block.
      note(
        `summary uses tables=${f.tables} headings=${f.headings} code=${f.codeBlocks}`,
      );
      check(
        "summary has rendered Markdown",
        f.tables + f.headings + f.codeBlocks > 0,
      );
      if (f.evidenceAt >= 0)
        check(
          "verification record shown as a literal code block",
          f.evidencePre,
        );
    }
    const tab2Final = await waitFor(async () => {
      const s = await timelineState(tab2);
      return s.finals > 0 ? s : null;
    }, 15000);
    check("second tab also shows the final result", !!tab2Final);
    await tab2.close();
    await layoutChecks(page, "desktop 1440x900");
    await shot(page, "s1-final");
    // Scroll the final into view for a readable screenshot of the card.
    await page
      .locator(".tl-final")
      .last()
      .scrollIntoViewIfNeeded()
      .catch(() => {});
    await page
      .locator(".tl-final")
      .last()
      .screenshot({ path: path.join(out, "s1-final-card.png") })
      .catch(() => {});
    check(
      "input usable after the run",
      (await timelineState(page)).inputDisabled === false,
    );
  });

  await runScenario("follow_up_in_same_session", async () => {
    const sessionsBefore = (await timelineState(page)).sessions.length;
    const start = await submit(
      page,
      `把这个服务的返回内容改成 hello-ui-2 并重启，旧进程要先停掉，端口不变。${policy}`,
    );
    check(
      "follow-up continues the selected session",
      start?.continueFrom === s1TaskId,
      JSON.stringify(start ?? {}).slice(0, 200),
    );
    check("idle wait", await waitServerIdle(page, 8 * 60000));
    await waitFor(async () => !(await timelineState(page)).stopVisible, 15000);
    const st = await timelineState(page);
    check(
      "no new session was created",
      st.sessions.length === sessionsBefore,
      `${sessionsBefore} -> ${st.sessions.length}`,
    );
    check(
      "both operator messages are in one timeline",
      st.users.length >= 2,
      JSON.stringify(st.users.map((u) => u.slice(0, 30))),
    );
    check(
      "second final reports success",
      st.lastFinalTitle?.startsWith("任务结束"),
      st.lastFinalTitle,
    );
    const svc = await httpText(`http://127.0.0.1:${s1Port}/`);
    check(
      "service now answers hello-ui-2",
      svc?.text === "hello-ui-2",
      JSON.stringify(svc),
    );
    await shot(page, "s2-final");
  });

  await runScenario(
    "history_while_running_then_stop_and_continue",
    async () => {
      await page.getByRole("button", { name: "新建会话", exact: true }).click();
      await waitFor(
        async () =>
          (await input(page).inputValue()) === "" &&
          (await timelineState(page)).actions === 0,
        5000,
      );
      const log = path.join(dir("s3"), "loop.log");
      const start = await submit(
        page,
        `在目录 ${dir("s3")}（不存在就创建）写 loop.py：每秒向同目录 loop.log 追加一行当前时间，共 300 秒。直接在前台运行 python loop.py（不要用 Start-Process，不要后台），运行时限设为 600 秒，等它结束再告诉我结果。${policy}`,
      );
      check(
        "new session start has no continueFrom",
        start && !start.continueFrom,
        JSON.stringify(start ?? {}).slice(0, 200),
      );
      const rec = start && (await waitAccepted(page, start.requestId));
      const growing = await waitFor(() => lineCount(log) >= 3, 240000, 1000);
      check("foreground loop is running", !!growing, `lines=${lineCount(log)}`);
      // Browse the older session while this one runs.
      const old = page.locator(`.session-item[data-task-id="${s1TaskId}"]`);
      check(
        "older session selectable while running",
        !(await old.isDisabled()),
      );
      await old.click();
      const browsing = await waitFor(async () => {
        const s = await timelineState(page);
        return s.finals >= 2 ? s : null;
      }, 10000);
      check("older session content shown while busy", !!browsing);
      check(
        "banner offers return to the running session",
        await page
          .getByRole("button", { name: "返回正在执行的会话" })
          .isVisible()
          .catch(() => false),
      );
      await shot(page, "s3-browsing-history");
      await page.getByRole("button", { name: "返回正在执行的会话" }).click();
      await waitFor(async () => (await timelineState(page)).stopVisible, 10000);
      const linesAtStop = lineCount(log);
      const stopAt = Date.now();
      await page.getByRole("button", { name: "停止任务", exact: true }).click();
      const stopped = await waitFor(async () => {
        const s = await timelineState(page);
        return !s.stopVisible ? s : null;
      }, 30000);
      const stopMs = Date.now() - stopAt;
      check("UI leaves running state after stop", !!stopped, `${stopMs}ms`);
      note(`stop took ${stopMs}ms; status="${stopped?.status}"`);
      await sleep(3000);
      const afterStop = lineCount(log);
      note(
        `log grew ${afterStop - linesAtStop} lines between the click and 3s after the UI settled`,
      );
      await sleep(3000);
      check(
        "stop really ended the foreground process",
        lineCount(log) === afterStop,
        `${afterStop} -> ${lineCount(log)}`,
      );
      const st = await timelineState(page);
      const shown = await page.evaluate(
        () => document.querySelector(".agent-main")?.innerText ?? "",
      );
      check(
        "timeline tells the operator the task was stopped",
        /已停止|已取消/.test(shown),
        st.status,
      );
      check(
        "terminal writable again after stop",
        st.termReadonly === "false",
        st.termReadonly,
      );
      check("input enabled after stop", st.inputDisabled === false);
      await shot(page, "s3-stopped");
      const cont = await submit(
        page,
        `改成用 Start-Process 在后台运行 loop.py，让它在后台继续跑满 300 秒，不要停止它；确认 loop.log 在增长后你就可以结束本次任务。${policy}`,
      );
      check(
        "continue after stop stays in the session",
        cont?.continueFrom === rec?.id,
        JSON.stringify(cont ?? {}).slice(0, 200),
      );
      check("idle wait", await waitServerIdle(page, 8 * 60000));
      await waitFor(
        async () => !(await timelineState(page)).stopVisible,
        15000,
      );
      const c1 = lineCount(log);
      await sleep(3000);
      check(
        "background loop keeps writing after the task ended",
        lineCount(log) > c1,
        `${c1} -> ${lineCount(log)}`,
      );
      check(
        "continued run reports success",
        (await timelineState(page)).lastFinalTitle?.startsWith("任务结束"),
        (await timelineState(page)).lastFinalTitle,
      );
      await finalCardChecks(page, "s3");
      await shot(page, "s3-continued");
    },
  );

  await runScenario("steer_while_running", async () => {
    await page.getByRole("button", { name: "新建会话", exact: true }).click();
    await waitFor(async () => (await timelineState(page)).actions === 0, 5000);
    const start = await submit(
      page,
      `在目录 ${dir("s4")}（不存在就创建）用 Python 标准库写 server.py，监听 127.0.0.1:18941，GET / 返回 alpha。先写一个 check.py 用 urllib 请求它并打印结果。后台启动服务，运行 check.py 验证。${policy}`,
    );
    const rec = start && (await waitAccepted(page, start.requestId));
    await page.locator(".tl-action").first().waitFor({ timeout: 180000 });
    check(
      "composer offers supplement while running",
      (await timelineState(page)).placeholder.includes("补充"),
      (await timelineState(page)).placeholder,
    );
    const steer = await submit(
      page,
      "改一下需求：端口改成 18942，返回内容改成 beta。18941 不要留着。",
      { click: true },
    );
    check(
      "supplement is sent as steer for this task",
      steer?.type === "steer" && steer.taskId === rec?.id,
      JSON.stringify(steer ?? {}).slice(0, 200),
    );
    const queued = await waitFor(async () => {
      const t = await page.locator(".steer-status").allInnerTexts();
      return t.length ? t.join(" | ") : null;
    }, 10000);
    check("steer status visible", !!queued, queued);
    const applied = await waitFor(
      async () => {
        const t = (await page.locator(".steer-status").allInnerTexts()).join(
          " ",
        );
        return /已应用/.test(t) ? t : null;
      },
      240000,
      1000,
    );
    check("steer applied to the running task", !!applied, applied);
    check("idle wait", await waitServerIdle(page, 8 * 60000));
    await waitFor(async () => !(await timelineState(page)).stopVisible, 15000);
    const beta = await httpText("http://127.0.0.1:18942/");
    const alpha = await httpText("http://127.0.0.1:18941/", 3000);
    check(
      "new requirement served on 18942",
      beta?.text === "beta",
      JSON.stringify(beta),
    );
    check("old port 18941 not left running", !alpha, JSON.stringify(alpha));
    const st = await timelineState(page);
    check(
      "steer shown in the timeline",
      st.users.some((u) => u.includes("18942")),
      JSON.stringify(st.users.map((u) => u.slice(0, 30))),
    );
    check(
      "steer run reports success",
      st.lastFinalTitle?.startsWith("任务结束"),
      st.lastFinalTitle,
    );
    await shot(page, "s4-steer");
  });

  await runScenario("needs_input_then_answer", async () => {
    await page.getByRole("button", { name: "新建会话", exact: true }).click();
    await waitFor(async () => (await timelineState(page)).actions === 0, 5000);
    const start = await submit(page, "帮我把服务部署到我们的测试服务器上。");
    const rec = start && (await waitAccepted(page, start.requestId));
    check("idle wait", await waitServerIdle(page, 6 * 60000));
    const st = await waitFor(async () => {
      const s = await timelineState(page);
      return s.finals ? s : null;
    }, 15000);
    check(
      "agent asks for the missing information",
      st?.lastFinalTitle === "等待补充信息" ||
        (st?.ending === "reply" && /[？?]/.test(st.lastFinalText)),
      `${st?.lastFinalTitle} ${st?.lastFinalText.slice(0, 120)}`,
    );
    check(
      "composer invites continuing",
      st?.placeholder.includes("继续"),
      st?.placeholder,
    );
    await shot(page, "s5-question");
    const ans = await submit(
      page,
      "先不部署了，你直接结束这个任务就行，不需要执行任何命令。",
    );
    check(
      "answer continues the asking session",
      ans?.continueFrom === rec?.id,
      JSON.stringify(ans ?? {}).slice(0, 200),
    );
    check("idle wait", await waitServerIdle(page, 4 * 60000));
    await waitFor(async () => !(await timelineState(page)).stopVisible, 15000);
    const end = await timelineState(page);
    check(
      "answer handled in the same timeline",
      end.users.length >= 2 && end.finals >= 1,
      JSON.stringify(end).slice(0, 300),
    );
    note(
      `last final: ${end.lastFinalTitle} / ${end.lastFinalText.slice(0, 200)}`,
    );
  });

  await runScenario("close_tab_while_running_and_return", async () => {
    await page.getByRole("button", { name: "新建会话", exact: true }).click();
    await waitFor(async () => (await timelineState(page)).actions === 0, 5000);
    const start = await submit(
      page,
      `在目录 ${dir("s6")}（不存在就创建）依次执行：1) 写 a.txt 内容 one；2) 运行 Start-Sleep -Seconds 20；3) 写 b.txt 内容 two；4) 用一条命令同时读取两个文件确认内容。${policy}`,
    );
    const rec = start && (await waitAccepted(page, start.requestId));
    await page.locator(".tl-action").first().waitFor({ timeout: 180000 });
    await page.close();
    note("tab closed while running");
    await sleep(25000);
    const p2 = await desktop.newPage();
    p2.setDefaultTimeout(15000);
    observe(p2, "reopened");
    await p2.goto(url);
    await input(p2).waitFor();
    const live = await waitFor(async () => {
      const s = await timelineState(p2);
      return s.actions > 0 ? s : null;
    }, 15000);
    check(
      "reopened tab shows the session's progress",
      !!live,
      JSON.stringify(await timelineState(p2)).slice(0, 300),
    );
    check("idle wait", await waitServerIdle(p2, 6 * 60000));
    const end = await waitFor(async () => {
      const s = await timelineState(p2);
      return s.finals && !s.stopVisible ? s : null;
    }, 15000);
    check("reopened tab receives the final", !!end);
    check(
      "run continued without a browser attached",
      fs.existsSync(path.join(dir("s6"), "b.txt")),
    );
    check(
      "final reports success",
      end?.lastFinalTitle?.startsWith("任务结束"),
      end?.lastFinalTitle,
    );
    check(
      "current session is the one that ran",
      end?.sessions.find((s) => s.current)?.id === rec?.id,
      JSON.stringify(end?.sessions),
    );
    await shot(p2, "s6-reopened");
    globalThis.mainPage = p2;
  });

  await runScenario("mobile_layout_and_run", async () => {
    const state = await desktop.storageState();
    const mobile = await browser.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
      storageState: state,
    });
    const m = await mobile.newPage();
    m.setDefaultTimeout(15000);
    observe(m, "mobile");
    await m.goto(url);
    await input(m).waitFor();
    await layoutChecks(m, "mobile 390x844 idle");
    await m.getByRole("button", { name: "展开会话导航", exact: true }).click();
    await m.locator(`.session-item[data-task-id="${s1TaskId}"]`).click();
    const s = await waitFor(async () => {
      const st = await timelineState(m);
      return st.finals >= 2 ? st : null;
    }, 10000);
    check("mobile opens the deploy session", !!s);
    await layoutChecks(m, "mobile deploy history");
    await m
      .locator(".tl-final")
      .last()
      .scrollIntoViewIfNeeded()
      .catch(() => {});
    await shot(m, "s7-mobile-final");
    await m.getByRole("button", { name: "展开会话导航", exact: true }).click();
    await m.getByRole("button", { name: "新建会话", exact: true }).click();
    const start = await submit(
      m,
      "用一条命令输出当前 PowerShell 版本号，然后结束。",
    );
    check("mobile can start a task", start?.type === "start");
    check("idle wait", await waitServerIdle(m, 4 * 60000));
    const end = await waitFor(async () => {
      const st = await timelineState(m);
      return st.finals && !st.stopVisible ? st : null;
    }, 15000);
    check("mobile shows the final", !!end);
    await layoutChecks(m, "mobile after run");
    await shot(m, "s7-mobile-run");
    await mobile.close();
  });

  await runScenario("guard_and_errors", async () => {
    const p = globalThis.mainPage;
    const g = await api(p, "/test/guard");
    note(`commands run: ${g.commands?.length}`);
    for (const h of g.hits ?? [])
      note(`guard refused: ${h.Command?.slice(0, 200)} -- ${h.Reason}`);
    check(
      "no page errors",
      pageErrors.length === 0,
      pageErrors.join(" | ").slice(0, 800),
    );
    check(
      "no console errors",
      consoleErrors.length === 0,
      consoleErrors.join(" | ").slice(0, 800),
    );
    check(
      "no failed HTTP responses",
      badResponses.length === 0,
      badResponses.join(" | ").slice(0, 800),
    );
  });

  fs.writeFileSync(
    path.join(out, "results.json"),
    JSON.stringify(results, null, 2),
  );
  const failed = results.filter((r) => !r.ok);
  console.log(
    `\nLIVE BROWSER: ${results.length - failed.length}/${results.length} checks passed`,
  );
  for (const f of failed)
    console.log(`  FAIL [${f.scenario}] ${f.name}: ${f.detail.slice(0, 300)}`);
  await browser.close();
  process.exitCode = failed.length ? 1 : 0;
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
