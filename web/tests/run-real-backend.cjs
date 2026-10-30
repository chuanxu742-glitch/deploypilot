const { spawn } = require("node:child_process");
const path = require("node:path");

const child = spawn(
  "go",
  [
    "test",
    "./internal/server",
    "-run",
    "^TestFrontendBrowserIntegration$",
    "-count=1",
    "-timeout=90s",
    "-v",
  ],
  {
    cwd: path.resolve(__dirname, "../../client"),
    env: { ...process.env, DP_BROWSER_INTEGRATION: "1" },
    stdio: "inherit",
    windowsHide: true,
  },
);
child.on("error", (error) => {
  console.error(error);
  process.exitCode = 1;
});
child.on("exit", (code) => {
  process.exitCode = code ?? 1;
});
