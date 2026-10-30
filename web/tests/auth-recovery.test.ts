import { test } from "node:test";
import assert from "node:assert/strict";
import { authenticate, listModels, onSessionExpired } from "../src/api.ts";

test("post-pair API 401 notifies the app and a disconnect probe reports expiry", async () => {
  let expired = 0;
  onSessionExpired(() => {
    expired += 1;
  });
  globalThis.fetch = async () => new Response("{}", { status: 401 });

  const { checkSessionAfterDisconnect } = await import("../src/api.ts");
  assert.equal(await checkSessionAfterDisconnect(), false);
  assert.equal(expired, 1);

  // The model-list request that exposed the defect uses the same expiry path.
  await assert.rejects(listModels(), /HTTP 401/);
  assert.equal(expired, 2);
  onSessionExpired(null);
});

test("disconnect probe keeps retrying when session status is unknown", async () => {
  const { checkSessionAfterDisconnect } = await import("../src/api.ts");
  globalThis.fetch = async () => {
    throw new TypeError("offline");
  };
  assert.equal(await checkSessionAfterDisconnect(), true);
});

test("concurrent disconnect checks share one session request", async () => {
  const { checkSessionAfterDisconnect } = await import("../src/api.ts");
  let requests = 0;
  let resolveResponse!: (response: Response) => void;
  globalThis.fetch = async () => {
    requests += 1;
    return new Promise<Response>((resolve) => {
      resolveResponse = resolve;
    });
  };

  const first = checkSessionAfterDisconnect();
  const second = checkSessionAfterDisconnect();
  assert.equal(requests, 1);
  resolveResponse(new Response("", { status: 503 }));
  assert.deepEqual(await Promise.all([first, second]), [true, true]);
});

test("revisiting a used pairing link accepts an already valid session first", async () => {
  const calls: string[] = [];
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { location: { hash: "#t=used-token", pathname: "/", search: "" } },
  });
  Object.defineProperty(globalThis, "history", {
    configurable: true,
    value: {
      replaceState: (_state: unknown, _title: string, url: string) => {
        calls.push(`clear:${url}`);
      },
    },
  });
  globalThis.fetch = async (input) => {
    calls.push(String(input));
    if (String(input) === "/api/session")
      return new Response("{}", { status: 200 });
    throw new Error(`unexpected request: ${String(input)}`);
  };

  assert.equal(await authenticate(), true);
  assert.deepEqual(calls, ["/api/session", "clear:/"]);
});

test("an invalid one-time token falls back to checking an existing session", async () => {
  const calls: string[] = [];
  let sessionChecks = 0;
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: { location: { hash: "#t=used-token", pathname: "/", search: "" } },
  });
  Object.defineProperty(globalThis, "history", {
    configurable: true,
    value: { replaceState: () => {} },
  });
  globalThis.fetch = async (input) => {
    calls.push(String(input));
    if (String(input) === "/api/pair")
      return new Response("{}", { status: 401 });
    if (String(input) === "/api/session")
      return new Response("{}", { status: ++sessionChecks === 1 ? 401 : 200 });
    throw new Error(`unexpected request: ${String(input)}`);
  };

  assert.equal(await authenticate(), true);
  assert.deepEqual(calls, ["/api/session", "/api/pair", "/api/session"]);
});
