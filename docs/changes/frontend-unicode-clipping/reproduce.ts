import { mergeEvent, normalizeEvents, MAX_EVENT_TEXT, TRUNCATED } from "../../../web/src/agent-state.ts";
const suffix = "a".repeat(MAX_EVENT_TEXT - TRUNCATED.length - 1);
const prefix = "x".repeat(TRUNCATED.length);
const deltas = [prefix + "🚀", suffix.slice(0, -1), suffix.slice(-1)];
console.log("Actual frontend mergeEvent/normalizeEvents; base main 9d5114c.");
console.log(`Valid Unicode chunks=${deltas.every(x => x.isWellFormed())}; largest chunk=${Math.max(...deltas.map(x => Buffer.byteLength(x)))} bytes`);
console.log(`Combined input=${deltas.join("").length} UTF-16 units; limit=${MAX_EVENT_TEXT}`);
for (const type of ["output", "thinking"]) {
  const events = deltas.map(text => ({type, text}));
  const live = events.reduce(mergeEvent, []);
  const restored = normalizeEvents(events);
  for (const [mode, text] of [["live",live[0].text!],["restored",restored[0].text!]]) {
    console.log(`${type}/${mode}: wellFormed=${text.isWellFormed()}; units=${text.length}; firstTailUnit=0x${text.charCodeAt(TRUNCATED.length).toString(16)}`);
  }
}
console.log("Expected: wellFormed=true; whole-character tail; units <= 32768.");
console.log("Production TypeScript functions; no browser UI, backend or model run.");
