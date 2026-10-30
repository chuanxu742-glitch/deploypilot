import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const css = readFileSync(new URL("../src/index.css", import.meta.url), "utf8");
const components = [
  "App",
  "Agent",
  "Timeline",
  "Settings",
  "Terminal",
  "ModelPicker",
  "Icon",
]
  .map((name) =>
    readFileSync(new URL(`../src/${name}.tsx`, import.meta.url), "utf8"),
  )
  .join("\n");

test("interface copy has no middle-dot separators or promotional captions", () => {
  assert.doesNotMatch(
    components,
    /·|YOUR DEPLOYMENT COPILOT|MODEL CONFIGURATION|REMOTE SESSION|全程协作|自在部署|有迹可循/,
  );
});

test("application icons use SVG instead of emoji or character pictograms", () => {
  assert.ok(!/[\p{Extended_Pictographic}\uFE0F\u2713]/u.test(components));
});
test("authored colors come from Radix rather than local color literals", () => {
  assert.doesNotMatch(css + components, /#[a-f\d]{3,8}\b|\brgba?\(|\bhsla?\(/i);
  for (const scale of ["gray", "blue", "slate-dark", "red", "amber"])
    assert.ok(css.includes(`@radix-ui/colors/${scale}.css`));
});
test("typography uses readable 14, 24 and 48 pixel sizes", () => {
  const sizes = [
    ...(css + components).matchAll(
      /(?:font-size\s*:|fontSize\s*:)\s*(\d+)px?/g,
    ),
  ].map((match) => Number(match[1]));
  assert.ok(sizes.every((size) => [14, 24, 48].includes(size)));
  const terminalSizes = [...components.matchAll(/fontSize:\s*(\d+)/g)].map(
    (match) => Number(match[1]),
  );
  assert.ok(terminalSizes.every((size) => [14, 24, 48].includes(size)));
  assert.doesNotMatch(css, /--text-small/);
  assert.match(css, /--text-body:\s*14px/);
  assert.match(css, /--text-title:\s*24px/);
  assert.match(css, /--text-display:\s*48px/);
});
test("visible borders use the half-pixel token", () => {
  assert.match(css, /--line:\s*0\.5px/);
  const declarations = [
    ...css.matchAll(
      /(?:^|[;{])\s*(?:border(?:-(?:top|bottom|left|right|width))?|outline)\s*:\s*([^;}]+)/g,
    ),
  ];
  for (const [, value] of declarations)
    assert.ok(/^(?:0\b|none\b|var\(--line\))/.test(value.trim()), value);
});
test("styles have no gradients, shadows, animations or transitions", () => {
  assert.doesNotMatch(
    css,
    /gradient\(|box-shadow\s*:|text-shadow\s*:|(?:^|[;{])\s*(?:transition|animation)\s*:/,
  );
  assert.match(components, /cursorBlink:\s*false/);
});

test("brand icons use Lucide and the desktop terminal precedes tasks", () => {
  assert.match(components, /from "lucide-react"/);
  const app = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
  assert.ok(app.indexOf('id="terminal-panel"') < app.indexOf("<AgentPanel"));
});

test("day, night, and comfort palettes are Radix-backed and theme selectors are present", () => {
  for (const scale of ["gray-dark", "blue-dark", "sand", "sand-dark"])
    assert.ok(css.includes(`@radix-ui/colors/${scale}.css`));
  assert.match(css, /data-theme="comfort"/);
  assert.match(components, /aria-label="外观"/);
});

test("desktop resizing and machine details are real controls, not decoration", () => {
  const app = readFileSync(new URL("../src/App.tsx", import.meta.url), "utf8");
  assert.match(app, /role="separator"/);
  assert.match(app, /machineInfo\(\)/);
  assert.match(app, /启动目录/);
});
