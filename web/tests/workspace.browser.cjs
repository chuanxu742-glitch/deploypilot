const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const path = require("node:path");
const { spawn } = require("node:child_process");
const { mkdirSync } = require("node:fs");
(async () => {
  const root = path.resolve(__dirname, "../..");
  process.chdir(root);
  mkdirSync(path.join(root, ".probe-tmp/ui-screenshots"), { recursive: true });
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
      "4175",
      "--strictPort",
    ],
    { cwd: path.join(root, "web"), stdio: "ignore" },
  );
  let browser;
  try {
    let ready = false;
    for (let attempt = 0; attempt < 100; attempt++) {
      if (preview.exitCode !== null)
        throw new Error("Preview server exited; port 4175 must be available.");
      try {
        if ((await fetch("http://127.0.0.1:4175")).ok) {
          ready = true;
          break;
        }
      } catch {}
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    if (!ready) throw new Error("Preview server did not start.");
    browser = await chromium.launch({ channel: "chrome", headless: true });
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
      deviceScaleFactor: 1,
    });
    if (process.env.DP_BROWSER_ARTIFACT_DIR) {
      const artifactDir = path.resolve(
        root,
        process.env.DP_BROWSER_ARTIFACT_DIR,
      );
      require("node:fs").mkdirSync(artifactDir, { recursive: true });
      const capture = page.screenshot.bind(page);
      page.screenshot = (options) =>
        capture({
          ...options,
          path: path.join(artifactDir, path.basename(options.path)),
        });
    }
    page.setDefaultTimeout(7000);
    await page
      .context()
      .grantPermissions(["clipboard-read", "clipboard-write"]);
    const copyTerminalLine = async (shortcut, expected) => {
      const screen = page.locator(".term .xterm-screen");
      const position = await screen.evaluate((el, text) => {
        const rows = Array.from(
          el.closest(".xterm").querySelectorAll(".xterm-rows > div"),
        );
        const row = rows.find((candidate) =>
          candidate.textContent.includes(text),
        );
        if (!row) throw new Error(`terminal row not found for copy: ${text}`);
        const screenBounds = el.getBoundingClientRect();
        const rowBounds = row.getBoundingClientRect();
        return {
          x: Math.max(10, rowBounds.left - screenBounds.left + 36),
          y: rowBounds.top - screenBounds.top + rowBounds.height / 2,
        };
      }, expected);
      await screen.click({ position, clickCount: 3 });
      const before = manualInputs.length;
      await page.keyboard.press(shortcut);
      const copied = await page.evaluate(() => navigator.clipboard.readText());
      assert.ok(
        copied.includes(expected),
        `${shortcut} must copy the selected terminal line; copied=${copied}`,
      );
      assert.equal(
        manualInputs.length,
        before,
        "copy must not send input or interrupt the shell",
      );
    };
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    let terminalConnections = 0,
      agentConnections = 0,
      starts = 0,
      agentSocket,
      terminalSocket;
    const manualInputs = [];
    const sizes = [];
    let taskSequence = 0;
    let agentState = "idle";
    let currentTask = null;
    let terminalResets = 0;
    const taskHistory = [];
    const eventSequences = new Map();
    const taskListReads = [];
    const taskReads = [];
    let terminalHistory = Buffer.from(
      "\u001b[32mDeployPilot remote session\u001b[0m\r\n\r\nWelcome to your deployment workspace.\r\nConnected to target machine.\r\n\r\noperator@server:~$ ",
    );
    const taskSummaries = () =>
      taskHistory.map(({ events, ...summary }) => summary);
    const sendSnapshot = (socket = agentSocket) =>
      socket?.send(
        JSON.stringify({
          type: "snapshot",
          state: agentState,
          task: currentTask,
          tasks: taskSummaries(),
        }),
      );
    const sendTerminalControl = (message) =>
      terminalSocket?.send(JSON.stringify(message));
    const sendTerminalOutput = (text) => {
      const bytes = Buffer.from(text);
      terminalHistory = Buffer.concat([terminalHistory, bytes]);
      terminalSocket?.send(bytes);
    };
    const sendAgentMessage = (message, socket = agentSocket) =>
      socket?.send(JSON.stringify(message));
    const setAgentState = (state) => {
      agentState = state;
      if (currentTask && state !== "idle") currentTask.phase = state;
      if (currentTask && state === "idle") {
        currentTask.phase = "finished";
        currentTask.endedAt = Date.now();
      }
      sendAgentMessage({
        type: "run_state",
        state,
        ...(currentTask ? { taskId: currentTask.id } : {}),
      });
      sendTerminalControl({ type: "agent_state", state });
    };
    const sendTaskEvent = (event, socket = agentSocket) => {
      const record = currentTask;
      if (!record) return;
      const id = (eventSequences.get(record.id) ?? 0) + 1;
      eventSequences.set(record.id, id);
      const complete = {
        ...event,
        id,
        taskId: record.id,
        timestamp: event.timestamp ?? Date.now(),
      };
      record.events.push(complete);
      sendAgentMessage(complete, socket);
    };
    const startTask = (title, socket, requestId) => {
      starts++;
      const id = `task-${++taskSequence}`;
      currentTask = {
        id,
        title,
        startedAt: Date.now(),
        phase: "running",
        events: [],
        requestId,
      };
      taskHistory.push(currentTask);
      eventSequences.set(id, 0);
      agentState = "running";
      sendTerminalControl({ type: "agent_state", state: "running" });
      sendSnapshot(socket);
      sendTaskEvent(
        { type: "activity", state: "generating", text: "等待模型响应" },
        socket,
      );
      sendTaskEvent(
        { type: "status", text: "正在检查机器环境…", step: 1 },
        socket,
      );
      sendTaskEvent({ type: "thinking", text: "我会先检查监听端口" }, socket);
      sendTaskEvent(
        { type: "activity", state: "executing", text: "执行工具" },
        socket,
      );
      sendTaskEvent(
        {
          type: "tool_call",
          tool: "run_command",
          command: "uname -a && df -h",
          callId: "cmd-1",
          hostname: "node-a",
          cwd: "/srv/app",
          step: 1,
        },
        socket,
      );
      sendTerminalOutput(
        "\r\nAgent 开始执行\r\n$ uname -a && df -h\r\nLinux server 6.8.0 x86_64\r\n/dev/sda1    100G    24G    76G    24% /\r\n/dev/sdb1    200G    80G    120G    40% /data\r\nMounted volumes and filesystem capacity\r\n",
      );
      sendTaskEvent(
        {
          type: "output",
          callId: "cmd-1",
          text: "Linux server 6.8.0 x86_64\n/dev/sda1    100G    24G    76G    24% /\n/dev/sdb1    200G    80G    120G    40G    40% /data\nMounted volumes and filesystem capacity\n",
        },
        socket,
      );
      sendTaskEvent(
        {
          type: "tool_done",
          callId: "cmd-1",
          tool: "run_command",
          exitCode: 0,
          durationMs: 42,
        },
        socket,
      );
      sendTaskEvent(
        {
          type: "error",
          callId: "cmd-1",
          text: "模拟可恢复工具错误，任务继续。",
        },
        socket,
      );
    };
    let config = { hasKey: true, model: "bailu-auto" };
    const longModel = `provider/${"very-long-model-".repeat(5)}`;
    const availableModels = [
      "bailu-auto",
      "test-model",
      ...Array.from(
        { length: 41 },
        (_, index) => `model-${String(index + 1).padStart(2, "0")}`,
      ),
      "model-43",
      longModel,
    ];
    let saves = 0;
    let delayModels = false,
      releaseModels;
    await page.route("**/api/**", async (route) => {
      const url = route.request().url();
      const pathname = new URL(url).pathname;
      if (pathname === "/api/tasks") {
        taskListReads.push(route.request().method());
        await route.fulfill({ json: taskSummaries() });
        return;
      }
      if (pathname.startsWith("/api/tasks/")) {
        const id = decodeURIComponent(pathname.slice("/api/tasks/".length));
        taskReads.push({ id, method: route.request().method() });
        const record = taskHistory.find((item) => item.id === id);
        if (!record) {
          await route.fulfill({ status: 404, json: { error: "not found" } });
          return;
        }
        const { ...copy } = record;
        await route.fulfill({ json: copy });
        return;
      }
      if (url.endsWith("/models") && delayModels)
        await new Promise((resolve) => {
          releaseModels = resolve;
        });
      let body = url.endsWith("/health")
        ? { status: "ok", version: "0.2.0" }
        : url.endsWith("/machine")
          ? {
              hostname: "node-a",
              platform: "linux/amd64",
              username: "deploy",
              cwd: "/srv/app",
              version: "1.2.3",
            }
          : url.endsWith("/models")
            ? { models: availableModels }
            : config;
      if (url.endsWith("/config")) {
        const patch = JSON.parse(route.request().postData());
        saves++;
        config = {
          hasKey: config.hasKey || !!patch.apiKey,
          model: patch.model || config.model,
        };
        body = config;
      }
      await route.fulfill({ json: body });
    });
    await page.routeWebSocket("**/ws/term", (socket) => {
      terminalConnections++;
      terminalSocket = socket;
      setTimeout(() => {
        terminalResets++;
        socket.send(JSON.stringify({ type: "terminal_reset" }));
        socket.send(JSON.stringify({ type: "agent_state", state: agentState }));
        socket.send(Buffer.from(terminalHistory));
      }, 20);
      socket.onMessage((data) => {
        if (typeof data !== "string") {
          const input = Buffer.from(data).toString();
          manualInputs.push(input);
          sendTerminalOutput(input);
          return;
        }
        try {
          const message = JSON.parse(String(data));
          if (message.type === "resize") sizes.push(message);
        } catch {}
      });
    });
    await page.routeWebSocket("**/ws/agent", (socket) => {
      agentConnections++;
      agentSocket = socket;
      sendSnapshot(socket);
      socket.onMessage((data) => {
        const message = JSON.parse(data);
        if (message.type === "start") {
          startTask(message.task, socket, message.requestId);
        }
        if (message.type === "stop") {
          setAgentState("stopping");
          currentTask.phase = "stopping";
          sendSnapshot(socket);
          setTimeout(
            () =>
              sendTaskEvent(
                { type: "final", text: "任务已停止。", success: false },
                socket,
              ),
            50,
          );
          setTimeout(() => {
            setAgentState("idle");
            sendSnapshot(socket);
          }, 400);
        }
      });
    });
    await page.goto("http://127.0.0.1:4175");
    await page.evaluate(() => document.fonts.ready);
    assert.match(
      await page
        .locator("body")
        .evaluate((el) => getComputedStyle(el).fontFamily),
      /Noto Sans SC Variable/,
    );
    await page.getByRole("heading", { name: "开始部署" }).waitFor();
    await page.getByText("已连接", { exact: true }).waitFor();
    await page.waitForFunction(() =>
      document
        .querySelector(".term .xterm-rows")
        ?.textContent.includes("DeployPilot remote session"),
    );
    await copyTerminalLine("Control+c", "DeployPilot remote session");
    await copyTerminalLine("Control+Shift+c", "DeployPilot remote session");
    await copyTerminalLine("Meta+c", "DeployPilot remote session");
    await page
      .locator(".term .xterm-screen")
      .click({ position: { x: 40, y: 8 } });
    const inputBeforeInterrupt = manualInputs.length;
    await page.keyboard.press("Control+c");
    assert.equal(
      manualInputs.slice(inputBeforeInterrupt).join(""),
      "\x03",
      "Ctrl+C without selection remains a terminal interrupt",
    );
    const inputBeforeCopyShortcut = manualInputs.length;
    await page.keyboard.press("Control+Shift+c");
    assert.equal(
      manualInputs.length,
      inputBeforeCopyShortcut,
      "copy-only shortcut without selection does not interrupt",
    );
    await page.locator(".machine-details summary").waitFor();
    await page.locator("#task-input").fill("多行任务\n".repeat(12));
    await page.getByRole("button", { name: "环境", exact: true }).click();
    assert.ok(
      (await page.locator("#task-input").boundingBox()).height < 100,
      "replacing a long draft with a short example resets composer height",
    );
    await page.locator("#task-input").fill("");
    await page.locator(".machine-details summary").click();
    await page.keyboard.press("Escape");
    assert.equal(
      await page.locator(".machine-details").evaluate((el) => el.open),
      false,
      "Escape dismisses device details",
    );
    await page.locator(".machine-details summary").click();
    await page.locator("#task-input").click();
    assert.equal(
      await page.locator(".machine-details").evaluate((el) => el.open),
      false,
      "outside click dismisses device details",
    );
    const initialTerminalConnections = terminalConnections;
    const taskWidth = async () =>
      (await page.locator("#agent-panel").boundingBox()).width;
    const beforeResize = await taskWidth();
    const splitter = page.getByRole("separator", { name: "调整面板宽度" });
    const splitterBounds = await splitter.boundingBox();
    await page.mouse.move(
      splitterBounds.x + splitterBounds.width / 2,
      splitterBounds.y + 80,
    );
    await page.mouse.down();
    await page.mouse.move(
      splitterBounds.x + splitterBounds.width / 2 + 32,
      splitterBounds.y + 80,
    );
    await page.mouse.up();
    assert.ok(
      (await taskWidth()) < beforeResize,
      "dragging separator grows the terminal",
    );
    const afterDrag = await taskWidth();
    await splitter.focus();
    await page.keyboard.press("ArrowRight");
    assert.ok(
      (await taskWidth()) < afterDrag,
      "ArrowRight grows the left terminal",
    );
    const selectTheme = async (value) => {
      const labels = { day: "日间", night: "夜间", comfort: "护眼" };
      await page.getByRole("button", { name: "外观设置", exact: true }).click();
      await page
        .getByRole("button", { name: labels[value], exact: true })
        .click();
      await page.keyboard.press("Escape");
    };
    assert.equal(
      await page.locator("html").getAttribute("data-theme"),
      "night",
      "first visit defaults to night even on a light system",
    );
    const checkContrast = async () => {
      const contrasts = await page.evaluate(() => {
        const luminance = (color) => {
          const values = color
            .match(/[\d.]+/g)
            .slice(0, 3)
            .map(Number)
            .map((v) => {
              const n = v / 255;
              return n <= 0.04045 ? n / 12.92 : ((n + 0.055) / 1.055) ** 2.4;
            });
          return values[0] * 0.2126 + values[1] * 0.7152 + values[2] * 0.0722;
        };
        return [
          [".brand-name", ".topbar"],
          [".hero h3", ".agent-panel"],
        ].map(([text, surface]) => {
          const fg = luminance(
            getComputedStyle(document.querySelector(text)).color,
          );
          const bg = luminance(
            getComputedStyle(document.querySelector(surface)).backgroundColor,
          );
          return (Math.max(fg, bg) + 0.05) / (Math.min(fg, bg) + 0.05);
        });
      });
      assert.ok(
        contrasts.every((value) => value >= 4.5),
        `theme contrast: ${contrasts}`,
      );
      const execute = page.getByRole("button", { name: "执行任务" });
      for (const state of ["normal", "hover", "focus", "disabled"]) {
        await page
          .locator("#task-input")
          .fill(state === "disabled" ? "" : "检查环境");
        await page.mouse.move(0, 0);
        if (state === "hover") await execute.hover();
        if (state === "focus") await execute.focus();
        const contrast = await execute.evaluate((el) => {
          const luminance = (color) => {
            const values = color
              .match(/[\d.]+/g)
              .slice(0, 3)
              .map(Number)
              .map((v) => {
                const n = v / 255;
                return n <= 0.04045 ? n / 12.92 : ((n + 0.055) / 1.055) ** 2.4;
              });
            return values[0] * 0.2126 + values[1] * 0.7152 + values[2] * 0.0722;
          };
          const style = getComputedStyle(el);
          const fg = luminance(style.color),
            bg = luminance(style.backgroundColor);
          return (Math.max(fg, bg) + 0.05) / (Math.min(fg, bg) + 0.05);
        });
        assert.ok(
          contrast >= 4.5,
          `${state} button text contrast: ${contrast}`,
        );
      }
    };
    await checkContrast();
    await selectTheme("night");
    await checkContrast();
    await page.locator("#task-input").fill("检查环境");
    await page.getByRole("button", { name: "执行任务" }).hover();
    const primaryColors = await page
      .getByRole("button", { name: "执行任务" })
      .evaluate((el) => {
        const style = getComputedStyle(el);
        return { text: style.color, background: style.backgroundColor };
      });
    assert.notEqual(
      primaryColors.text,
      primaryColors.background,
      "hovered primary button text must remain visible in night mode",
    );
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/night-button-hover.png"),
    });
    await page.locator("#task-input").fill("");
    await page.mouse.move(0, 0);
    assert.equal(
      await page.locator("html").getAttribute("data-theme"),
      "night",
    );
    const nightSurface = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue("--gray-1"),
    );
    assert.equal(
      terminalConnections,
      initialTerminalConnections,
      "theme changes preserve terminal socket",
    );
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/night.png"),
    });
    await selectTheme("comfort");
    await checkContrast();
    assert.equal(
      await page.locator("html").getAttribute("data-theme"),
      "comfort",
    );
    const comfortSurface = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue("--gray-1"),
    );
    assert.notEqual(comfortSurface, nightSurface);
    assert.equal(
      await page.evaluate(() => localStorage.getItem("deploypilot:theme")),
      "comfort",
    );
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/comfort.png"),
    });
    await selectTheme("day");
    await page.reload();
    await page.getByRole("heading", { name: "开始部署" }).waitFor();
    assert.equal(await page.locator("html").getAttribute("data-theme"), "day");
    const auditStyles = async () => {
      const audit = await page.evaluate(() => {
        const visible = Array.from(document.querySelectorAll("body *")).filter(
          (el) =>
            el instanceof HTMLElement &&
            !el.closest(".xterm") &&
            el.getClientRects().length &&
            getComputedStyle(el).visibility !== "hidden",
        );
        const text = visible.filter(
          (el) => el.textContent.trim() || el.matches("input,textarea"),
        );
        const fontSizes = [
          ...new Set(text.map((el) => getComputedStyle(el).fontSize)),
        ];
        const probe = document.createElement("span");
        document.body.append(probe);
        const palette = new Set(["rgba(0, 0, 0, 0)"]);
        for (const name of [
          "gray",
          "slate",
          "green",
          "red",
          "amber",
          "blue",
          "black-a",
        ])
          for (let step = 1; step <= 12; step++) {
            const token = `--${name}${name.endsWith("-a") ? "" : "-"}${step}`;
            for (const source of [
              document.body,
              document.querySelector(".term-panel"),
            ]) {
              const value = getComputedStyle(source)
                .getPropertyValue(token)
                .trim();
              if (value) {
                probe.style.color = value;
                palette.add(getComputedStyle(probe).color);
              }
            }
          }
        probe.remove();
        const unexpectedColors = [];
        for (const el of visible) {
          const style = getComputedStyle(el);
          for (const property of ["color", "backgroundColor"])
            if (!palette.has(style[property]))
              unexpectedColors.push(
                `${el.className}: ${property}=${style[property]}`,
              );
        }
        const centers = Array.from(document.querySelectorAll(".panel-head"))
          .filter(
            (el) =>
              el.getClientRects().length &&
              el.querySelector(".panel-title > svg"),
          )
          .map((el) => {
            const icon = el
                .querySelector(".panel-title > svg")
                .getBoundingClientRect(),
              title = el.querySelector("h2").getBoundingClientRect();
            return {
              icon: [icon.x, icon.y, icon.width, icon.height],
              title: [title.x, title.y, title.width, title.height],
            };
          });
        return {
          fontSizes,
          unexpectedColors,
          centers,
          text: document.body.innerText,
        };
      });
      assert.ok(audit.fontSizes.length <= 3, JSON.stringify(audit.fontSizes));
      assert.ok(
        audit.fontSizes.every((size) =>
          ["14px", "24px", "48px"].includes(size),
        ),
        JSON.stringify(audit.fontSizes),
      );
      assert.deepEqual(audit.unexpectedColors, []);
      assert.ok(
        audit.centers.every(
          (rects) =>
            Math.abs(
              rects.icon[1] +
                rects.icon[3] / 2 -
                rects.title[1] -
                rects.title[3] / 2,
            ) <= 1,
        ),
        JSON.stringify(audit.centers),
      );
      assert.ok(!audit.text.includes("·"));
    };
    await auditStyles();
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/desktop.png"),
    });
    assert.equal(
      await page.locator("#terminal-panel").isVisible(),
      true,
      "desktop keeps the terminal visible beside the task",
    );
    assert.equal(await page.locator("#agent-panel").isVisible(), true);
    const taskBounds = await page.locator("#agent-panel").boundingBox();
    const terminalBounds = await page.locator("#terminal-panel").boundingBox();
    assert.ok(
      terminalBounds.x < taskBounds.x,
      "terminal must stay left of tasks",
    );
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/workspace-split.png"),
    });
    assert.equal(
      terminalConnections,
      initialTerminalConnections + 1,
      "theme reload creates only the normal new page connection",
    );
    assert.equal(
      await page.getByRole("button", { name: "执行任务" }).isEnabled(),
      false,
    );
    await page.locator(".model-button").click();
    await auditStyles();
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/settings.png"),
    });
    assert.equal(await page.getByLabel("模型名称").inputValue(), "bailu-auto");
    await page.getByRole("button", { name: "获取列表" }).click();
    await page.getByText("已获取 45 个模型", { exact: false }).waitFor();
    assert.equal(
      await page.locator(".model-option").count(),
      45,
      "opening the model menu must expose all fetched models",
    );
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/model-list.png"),
    });
    await page.getByRole("option", { name: longModel, exact: true }).click();
    assert.equal(await page.getByLabel("模型名称").inputValue(), longModel);
    await page
      .getByRole("button", { name: "展开模型列表", exact: true })
      .click();
    assert.equal(await page.locator(".model-option").count(), 45);
    await page.getByLabel("模型名称").press("Home");
    await page.getByLabel("模型名称").press("Enter");
    assert.equal(await page.getByLabel("模型名称").inputValue(), "bailu-auto");
    await page.getByLabel("模型名称").fill("TEST");
    assert.equal(await page.locator(".model-option").count(), 1);
    await page.getByLabel("模型名称").press("Enter");
    assert.equal(await page.getByLabel("模型名称").inputValue(), "test-model");
    await page.getByLabel("模型名称").fill("not-a-listed-model");
    assert.equal(await page.locator(".model-option").count(), 0);
    await page.getByText("未找到匹配模型", { exact: true }).waitFor();
    await page.getByLabel("模型名称").press("Escape");
    assert.equal(await page.getByRole("dialog").isVisible(), true);
    await page.getByLabel("模型名称").fill("test-model");
    const savesBeforeClick = saves;
    await page.getByRole("button", { name: "保存设置" }).click();
    await page.getByText("已保存", { exact: true }).waitFor();
    assert.equal(
      saves,
      savesBeforeClick + 1,
      "saving while the model menu is open must work on the first click",
    );
    await page.keyboard.press("Escape");
    await page.locator(".model-button").click();
    assert.equal(await page.getByLabel("模型名称").inputValue(), "test-model");
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "环境", exact: true }).click();
    const input = page.locator("#task-input");
    await input.press("Shift+Enter");
    assert.ok(
      (await input.inputValue()).includes("\n"),
      "Shift+Enter inserts a newline",
    );
    await input.dispatchEvent("keydown", { key: "Enter", isComposing: true });
    await input.dispatchEvent("keydown", { key: "Enter", keyCode: 229 });
    assert.equal(starts, 0, "IME confirmation must not submit");
    await input.press("Enter");
    await page
      .locator("#agent-panel")
      .getByText("模拟可恢复工具错误，任务继续。")
      .waitFor();
    assert.equal(
      await page
        .locator("#task-input")
        .evaluate((el) => el === document.activeElement),
      true,
      "sending keeps focus in the composer",
    );
    const terminalText = await page.locator(".term .xterm-rows").textContent();
    const terminalRows = await page.locator(".term .xterm-rows").textContent();
    assert.ok(
      terminalText.includes("uname -a && df -h") &&
        terminalText.includes("Mounted volumes"),
      `the single terminal receives actual agent commands and output: a11y=${terminalText}; rows=${terminalRows}`,
    );
    assert.equal(
      await page.locator(".term .xterm:visible").count(),
      1,
      "only one terminal is visible",
    );
    sendTerminalOutput("manual shell preserved\r\n");
    assert.equal(
      await page.locator(".term").getAttribute("data-readonly"),
      "true",
    );
    await copyTerminalLine("Control+c", "Agent 开始执行");
    await copyTerminalLine("Control+Shift+c", "Agent 开始执行");
    await copyTerminalLine("Meta+c", "Agent 开始执行");
    assert.equal(
      await page.locator(".term .xterm:visible").count(),
      1,
      "copy does not switch away from agent output",
    );
    const sentBefore = manualInputs.length;
    await page.locator(".term .xterm-helper-textarea").focus();
    await page.keyboard.type("must-not-run");
    await page.keyboard.press("Enter");
    assert.equal(
      manualInputs.length,
      sentBefore,
      "manual typing is suppressed during agent execution",
    );
    await page.getByText("执行工具", { exact: true }).waitFor();
    await page.getByText("我会先检查监听端口", { exact: true }).waitFor();
    sendTaskEvent({
      type: "final",
      state: "reply",
      text: "我会先检查监听端口",
      success: false,
    });
    sendTaskEvent({ type: "status", text: "普通回复已结束" });
    await page.getByText("普通回复已结束", { exact: true }).waitFor();
    assert.equal(
      await page.locator(".tl-final, .final-card").count(),
      0,
      "a reply final is already represented by its streamed assistant message",
    );
    assert.equal(await page.locator(".tl-error").count(), 0);
    sendTaskEvent({
      type: "final",
      state: "needs_input",
      text: "请补充目标服务地址。",
      success: false,
    });
    sendTaskEvent({ type: "status", text: "补充信息状态已收到" });
    await page.getByText("补充信息状态已收到", { exact: true }).waitFor();
    const needsInput = page
      .locator(".tl-final")
      .filter({ hasText: "等待补充信息" });
    await needsInput.waitFor();
    await needsInput.getByText("请补充目标服务地址。", { exact: true }).waitFor();
    assert.equal(await needsInput.locator(".tl-error").count(), 0);
    await page.locator(".machine-details summary").waitFor();
    await page
      .locator(".action-evidence")
      .getByText("启动目录 /srv/app", { exact: true })
      .waitFor();
    await page.getByText("42 ms", { exact: true }).waitFor();
    assert.equal(
      await page.getByRole("button", { name: "停止任务" }).isEnabled(),
      true,
    );
    assert.equal(starts, 1, "switching views must not restart the task");
    assert.equal(
      await page.locator(".task-text").innerText(),
      "检查目标机器的系统版本、磁盘、内存和 GPU 环境",
    );
    assert.equal(
      await page.getByRole("button", { name: "停止任务" }).isEnabled(),
      true,
    );
    assert.equal(await page.locator(".model-button").isEnabled(), false);
    const commandDetails = page.locator(".action").first();
    assert.equal(
      await commandDetails.evaluate((el) => el.open),
      true,
      "command errors open execution evidence",
    );
    await commandDetails.locator("summary").click();
    assert.equal(
      await commandDetails.evaluate((el) => el.open),
      false,
      "execution evidence can be collapsed as a unit",
    );
    await commandDetails.locator("summary").click();
    assert.equal(await page.locator(".action-output pre").isVisible(), true);
    await page.getByPlaceholder("补充当前任务").fill("下一步检查服务");
    await page.getByPlaceholder("补充当前任务").press("Enter");
    assert.ok(
      (await page.locator("#task-input").inputValue()).includes("\n"),
      "Enter while preparing a draft during execution inserts a newline",
    );
    assert.equal(
      await page.getByRole("button", { name: "执行任务" }).isEnabled(),
      false,
    );
    await page.getByPlaceholder("补充当前任务").press("Control+Enter");
    assert.equal(starts, 1, "drafting during a run cannot start another task");
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/task-running.png"),
    });
    for (let i = 0; i < 45; i++)
      sendTaskEvent({
        type: "status",
        text: `检查步骤 ${i}：日志滚动测试`,
        step: 2,
      });
    await page.getByText("检查步骤 44：日志滚动测试").waitFor();
    await page.locator(".agent-scroll").evaluate((el) => {
      el.scrollTop = 0;
      el.dispatchEvent(new Event("scroll"));
    });
    await page.getByRole("button", { name: "返回最新记录" }).waitFor();
    await page.getByRole("button", { name: "返回最新记录" }).click();
    await page.getByRole("button", { name: "停止任务" }).click();
    const failedFinal = page.locator(".tl-final.tl-error");
    await failedFinal.getByText("任务未完成", { exact: true }).waitFor();
    await failedFinal.getByText("任务已停止。", { exact: true }).waitFor();
    assert.equal(
      await page.locator(".term").getAttribute("data-readonly"),
      "true",
      "stop request does not release terminal input before cleanup",
    );
    assert.equal(
      await page.getByRole("button", { name: "正在停止…" }).isEnabled(),
      false,
    );
    await page.waitForFunction(
      () => !document.querySelector(".composer .primary")?.disabled,
    );
    await page.waitForFunction(
      () => document.querySelector(".term")?.dataset.readonly === "false",
    );
    await page.locator(".term .xterm-helper-textarea").focus();
    await page.keyboard.type("manual-after-cleanup");
    await page.waitForFunction(() =>
      document.activeElement?.classList.contains("xterm-helper-textarea"),
    );
    assert.ok(
      manualInputs.join("").includes("manual-after-cleanup"),
      "terminal input resumes after cleanup",
    );
    await page.waitForFunction(() =>
      document
        .querySelector(".term .xterm-rows")
        ?.textContent.includes("manual shell preserved"),
    );
    assert.ok(
      (await page.locator(".term .xterm-rows").textContent()).includes(
        "Welcome to your deployment workspace",
      ),
      "manual screen and cursor context survive agent output",
    );
    await page.locator("#task-input").fill("重新检查部署环境");
    await page.getByRole("button", { name: "执行任务" }).click();
    await page.getByRole("button", { name: "停止任务" }).waitFor();
    assert.equal(starts, 2);
    await page.setViewportSize({ width: 390, height: 844 });
    const stopBounds = await page.locator(".stop-compact").boundingBox();
    assert.ok(
      stopBounds.x >= 0 && stopBounds.x + stopBounds.width <= 390,
      JSON.stringify(stopBounds),
    );
    assert.ok(
      await page.evaluate(
        () =>
          document.querySelector(".theme-control").getBoundingClientRect()
            .right <= innerWidth,
      ),
    );
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/task-running-mobile.png"),
    });
    const startsBeforeReconnect = starts;
    const connectionsBeforeReconnect = agentConnections;
    agentSocket.close();
    for (
      let attempt = 0;
      attempt < 80 && agentConnections === connectionsBeforeReconnect;
      attempt++
    )
      await page.waitForTimeout(50);
    assert.ok(
      agentConnections > connectionsBeforeReconnect,
      "Agent WS reconnects automatically after a disconnect",
    );
    await page.getByRole("button", { name: "停止任务" }).waitFor();
    assert.equal(
      starts,
      startsBeforeReconnect,
      "snapshot recovery never repeats start",
    );
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(await page.locator("#agent-panel").isVisible(), true);
    assert.equal(await page.locator("#terminal-panel").isVisible(), false);
    await page.getByRole("button", { name: "远程终端", exact: true }).click();
    assert.equal(await page.locator("#terminal-panel").isVisible(), true);
    for (
      let i = 0;
      i < 40 &&
      !(sizes.length >= 2 && sizes[sizes.length - 1].cols < sizes[0].cols);
      i++
    )
      await page.waitForTimeout(50);
    assert.ok(sizes.length >= 2);
    assert.ok(
      sizes[sizes.length - 1].cols > 0 &&
        sizes[sizes.length - 1].cols < sizes[0].cols,
    );
    const connectionsBeforeTerminalReconnect = terminalConnections;
    const resetsBeforeTerminalReconnect = terminalResets;
    // The terminal reconnects on its own after a dropped connection.
    terminalSocket.close();
    for (
      let attempt = 0;
      attempt < 80 &&
      terminalConnections === connectionsBeforeTerminalReconnect;
      attempt++
    )
      await page.waitForTimeout(50);
    assert.ok(
      terminalConnections > connectionsBeforeTerminalReconnect,
      "terminal reconnects to the shared PTY without a click",
    );
    for (
      let attempt = 0;
      attempt < 80 && terminalResets === resetsBeforeTerminalReconnect;
      attempt++
    )
      await page.waitForTimeout(50);
    assert.equal(terminalResets, resetsBeforeTerminalReconnect + 1);
    await page.waitForFunction(() =>
      document
        .querySelector(".term .xterm-rows")
        ?.textContent.includes("Mounted volumes"),
    );
    const terminalAfterReconnect = await page
      .locator(".term .xterm-rows")
      .textContent();
    assert.ok(
      terminalAfterReconnect.includes("uname -a && df -h") &&
        terminalAfterReconnect.includes("Mounted volumes"),
      "terminal reconnect replays the same shell history",
    );
    assert.equal(
      terminalAfterReconnect.split("DeployPilot remote session").length - 1,
      1,
      "terminal_reset clears the old buffer before replay",
    );
    assert.equal(
      starts,
      startsBeforeReconnect,
      "terminal reconnect never starts or restarts the Agent task",
    );
    const connectionsBeforeTabSwitch = terminalConnections;
    await page.getByRole("button", { name: "部署任务", exact: true }).click();
    assert.equal(terminalConnections, connectionsBeforeTabSwitch);
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
      true,
    );
    const terminalConnectionsBeforeRefresh = terminalConnections;
    const terminalResetsBeforeRefresh = terminalResets;
    await page.reload();
    await page
      .getByRole("heading", { name: "重新检查部署环境", exact: true })
      .waitFor();
    await page.getByRole("button", { name: "停止任务" }).waitFor();
    assert.equal(
      starts,
      startsBeforeReconnect,
      "refresh restores a running task without resending start",
    );
    assert.ok(terminalConnections > terminalConnectionsBeforeRefresh);
    assert.equal(
      terminalResets,
      terminalResetsBeforeRefresh + 1,
      "each PTY reconnection starts with terminal_reset",
    );
    await page.waitForFunction(() =>
      document
        .querySelector(".term .xterm-rows")
        ?.textContent.includes("Mounted volumes"),
    );
    const restoredTerminalRows = await page
      .locator(".term .xterm-rows")
      .textContent();
    assert.equal(
      restoredTerminalRows.split("DeployPilot remote session").length - 1,
      1,
      "terminal reset and history replay do not duplicate the prior buffer",
    );
    assert.ok(
      restoredTerminalRows.includes("uname -a && df -h") &&
        restoredTerminalRows.includes("Mounted volumes"),
      "refresh replays shared PTY history",
    );
    await page.getByRole("button", { name: "停止任务" }).click();
    await page.waitForFunction(
      () => !document.querySelector(".composer .primary")?.disabled,
    );
    assert.equal(
      starts,
      startsBeforeReconnect,
      "stopping and cleanup never repeat start",
    );
    const startsBeforeHistory = starts;
    const taskReadsBeforeHistory = taskReads.length;
    await page.getByLabel("任务历史").selectOption("task-1");
    await page
      .getByRole("heading", {
        name: "检查目标机器的系统版本、磁盘、内存和 GPU 环境",
        exact: true,
      })
      .waitFor();
    assert.equal(taskReads.length, taskReadsBeforeHistory + 1);
    assert.deepEqual(
      taskReads.at(-1),
      { id: "task-1", method: "GET" },
      "history selection loads a record with GET only",
    );
    sendSnapshot(agentSocket);
    await page.waitForTimeout(50);
    assert.equal(
      await page.locator(".task-text").innerText(),
      "检查目标机器的系统版本、磁盘、内存和 GPU 环境",
      "idle task snapshots do not replace an older selected record",
    );
    assert.equal(
      starts,
      startsBeforeHistory,
      "browsing task history never starts a task",
    );
    await page.getByRole("button", { name: "新建任务" }).click();
    await page.getByRole("heading", { name: "开始部署" }).waitFor();
    await auditStyles();
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/mobile.png"),
    });
    await page.locator(".model-button").click();
    assert.equal(await page.getByRole("dialog").isVisible(), true);
    await auditStyles();
    assert.equal(
      await page
        .getByRole("dialog")
        .evaluate((el) => el.getBoundingClientRect().right <= innerWidth),
      true,
    );
    await page.keyboard.press("Escape");
    config = { hasKey: false, model: "bailu-auto" };
    await page.reload();
    await page.getByRole("heading", { name: "开始部署" }).waitFor();
    await page.locator(".model-button").click();
    assert.equal(
      await page.getByRole("button", { name: "获取列表" }).isEnabled(),
      false,
    );
    const beforeSave = saves;
    await page.getByLabel("API Key", { exact: true }).fill("test-key");
    await page.getByRole("button", { name: "获取列表" }).click();
    await page.getByText("已获取 45 个模型", { exact: true }).waitFor();
    assert.equal(saves, beforeSave + 1);
    assert.equal(
      await page.getByLabel("API Key", { exact: true }).inputValue(),
      "",
    );
    assert.equal(await page.locator(".model-option").count(), 45);
    await page.getByRole("option", { name: longModel, exact: true }).click();
    assert.equal(await page.getByLabel("模型名称").inputValue(), longModel);
    await page.screenshot({
      path: path.resolve(".probe-tmp/ui-screenshots/model-mobile.png"),
    });
    await page.keyboard.press("Escape");
    for (const viewport of [
      { width: 320, height: 568 },
      { width: 820, height: 600 },
      { width: 1024, height: 768 },
      { width: 1920, height: 1080 },
    ]) {
      await page.setViewportSize(viewport);
      await page.evaluate(() => new Promise(requestAnimationFrame));
      await auditStyles();
      if (viewport.width === 320) {
        const execute = await page
          .getByRole("button", { name: "执行任务" })
          .boundingBox();
        assert.ok(
          execute.x >= 0 && execute.x + execute.width <= viewport.width,
          JSON.stringify(execute),
        );
      }
      if (viewport.width === 1024)
        assert.ok(
          (await page.locator("#agent-panel").boundingBox()).width >= 360,
          "task panel keeps its minimum desktop width",
        );
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
        true,
      );
    }
    await page.setViewportSize({ width: 390, height: 420 });
    await page.locator("#task-input").fill("检查服务\n".repeat(15));
    const compactInput = await page.locator("#task-input").boundingBox();
    assert.ok(
      compactInput.height <= 120,
      "short viewport caps the composer height",
    );
    await page
      .getByRole("button", { name: "执行任务" })
      .scrollIntoViewIfNeeded();
    const compactSend = await page
      .getByRole("button", { name: "执行任务" })
      .boundingBox();
    assert.ok(
      compactSend.y >= 100 && compactSend.y + compactSend.height <= 420,
      "send remains reachable in a short mobile viewport",
    );
    await page.locator("#task-input").fill("");
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole("button", { name: "模型设置", exact: true }).click();
    delayModels = true;
    await page.getByRole("button", { name: "获取列表", exact: true }).click();
    await page.getByRole("button", { name: "获取中…", exact: true }).waitFor();
    await page.keyboard.press("Escape");
    assert.equal(
      await page.locator("dialog[open]").count(),
      0,
      "a slow model request must not trap the user in the settings dialog",
    );
    releaseModels();
    delayModels = false;
    await page.waitForFunction(
      () =>
        !document.querySelector(".settings-dialog form .secondary").disabled,
    );
    assert.notEqual(
      await page.evaluate(() => document.activeElement?.id),
      "model",
      "late model results do not steal focus into a closed dialog",
    );
    await page.getByRole("button", { name: "模型设置", exact: true }).click();
    delayModels = true;
    await page.clock.install();
    await page.getByRole("button", { name: "获取列表", exact: true }).click();
    await page.getByRole("button", { name: "获取中…", exact: true }).waitFor();
    await page.clock.runFor(20_100);
    await page.getByText("请求超时，请重试。", { exact: true }).waitFor();
    assert.equal(
      await page
        .getByRole("button", { name: "获取列表", exact: true })
        .isEnabled(),
      true,
      "timeout makes retry available",
    );
    releaseModels();
    await page.getByRole("button", { name: "关闭设置", exact: true }).click();
    console.log(
      "PASS: composer height/focus, busy draft Enter, device dismissal, short mobile viewport, delayed settings close and timeout recovery.",
    );
    assert.deepEqual(errors, []);
    console.log(
      "PASS: desktop/mobile layout, current model, save/fetch, examples, recoverable error, folding, follow, stop acknowledgement, restart, disconnect, persistent mobile terminal, no page errors.",
    );
  } finally {
    await browser?.close();
    preview.kill();
  }
})().catch((error) => {
  console.error(error);
  process.exit(1);
});
