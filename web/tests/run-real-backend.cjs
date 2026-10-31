const { spawn } = require("node:child_process");
const path = require("node:path");

const child = spawn(
  "go",
  [
    "test",
    "./internal/server",
    "-run",
    // Every *FrontendBrowserIntegration test; each bounds its own browser run.
    "^Test\\w*FrontendBrowserIntegration$",
    "-count=1",
    "-timeout=5m",
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
