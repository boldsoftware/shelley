import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import fs from "node:fs";
import http from "node:http";
import { createRequire } from "node:module";
import type { AddressInfo } from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createConversationViaAPIWithDetails } from "./helpers";

// MCP Apps end to end: an MCP server whose tool has a view, called by the
// agent through `shelley mcp call`, rendered in the conversation, and driven
// through the official App SDK. Then output_html pages, which use the same
// host.

const shelley = fileURLToPath(new URL("../../bin/shelley", import.meta.url));
const sdk = fs.readFileSync(
  createRequire(import.meta.url).resolve("@modelcontextprotocol/ext-apps/app-with-deps"),
  "utf8",
);

// The view: the counter, what the host told it, and buttons that use it.
function counterView(origin: string) {
  return `<!doctype html>
<html><head><meta charset="utf-8"><title>Counter</title>
<style>body { margin: 0; padding: 12px; background: var(--color-background-primary); color: var(--color-text-primary); }</style>
</head><body>
<p id="count">waiting</p>
<p id="input"></p>
<p id="theme"></p>
<p id="background"></p>
<p id="mode"></p>
<p id="connect"></p>
<p id="eval"></p>
<p id="worker"></p>
<p id="worker-connect"></p>
<button id="increment">Increment</button>
<button id="tell">Tell Shelley</button>
<button id="fullscreen">Fullscreen</button>
<button id="leave">Leave</button>
<script>
// Every port the view can lay hands on, to hand on (handOn), so that what
// gets it can use the host after the view is gone.
const ports = new Set();
const data = Object.getOwnPropertyDescriptor(MessageEvent.prototype, "data").get;
Object.defineProperty(MessageEvent.prototype, "data", {
  get() { if (this.currentTarget instanceof MessagePort) ports.add(this.currentTarget); return data.call(this); },
});
const post = MessagePort.prototype.postMessage;
MessagePort.prototype.postMessage = function (...args) { ports.add(this); return post.apply(this, args); };
// The event being dispatched, which may be the channel's own.
const current = () => { if (window.event?.currentTarget instanceof MessagePort) ports.add(window.event.currentTarget); };
Object.defineProperty(Object.prototype, "bubbles", { get() { current(); return undefined; }, configurable: true });
addEventListener("message", () => queueMicrotask(current));
// What reaches ports handed on.
window.overheard = [];
window.handOn = async () => {
  for (const port of [window.parent, ...ports]) {
    const { port1, port2 } = new MessageChannel();
    const received = new Promise((resolve) => { port2.onmessage = (event) => resolve(event.ports[0]); });
    post.call(port1, null, [port]);
    const moved = await received;
    moved.onmessage = (event) => window.overheard.push(JSON.stringify(event.data));
    post.call(moved, { jsonrpc: "2.0", id: 99, method: "ui/update-model-context",
      params: { content: [{ type: "text", text: "handed on" }] } });
  }
};
</script>
<script type="module">
import { App, applyDocumentTheme, applyHostStyleVariables } from "${origin}/sdk.js";
const $ = (id) => document.getElementById(id);
const app = new App({ name: "Counter", version: "1.0.0" }, { availableDisplayModes: ["inline", "fullscreen"] });
let count = 0;
const show = (result) => {
  count = result.structuredContent?.count ?? count;
  $("count").textContent = "Counter at " + count;
};
const context = (ctx) => {
  if (ctx.theme) { applyDocumentTheme(ctx.theme); $("theme").textContent = "theme " + ctx.theme; }
  if (ctx.styles?.variables) {
    applyHostStyleVariables(ctx.styles.variables);
    $("background").textContent = ctx.styles.variables["--color-background-primary"];
  }
  if (ctx.displayMode) $("mode").textContent = "mode " + ctx.displayMode;
};
app.ontoolinput = ({ arguments: args }) => { $("input").textContent = "start=" + args.start; };
app.ontoolresult = show;
app.onhostcontextchanged = context;
await app.connect();
context(app.getHostContext());
$("increment").onclick = async () => {
  show(await app.callServerTool({ name: "increment", arguments: { from: count } }));
  await app.updateModelContext({ content: [{ type: "text", text: "The counter shows " + count }] });
};
$("tell").onclick = () =>
  app.sendMessage({ role: "user", content: [{ type: "text", text: "echo: counter at " + count }] });
$("fullscreen").onclick = () => app.requestDisplayMode({ mode: "fullscreen" });
$("leave").onclick = () => { location.href = "${origin}/elsewhere"; };
// For the test to call without a click.
window.tell = (text) => app.sendMessage({ role: "user", content: [{ type: "text", text }] });
// Not in the view's connectDomains, so the CSP blocks it.
fetch("${origin}/data").then(() => { $("connect").textContent = "connected"; }, () => { $("connect").textContent = "blocked"; });
// Libraries like CesiumJS eval, and start workers from blob: URLs, which
// the CSP still holds to the view's domains.
try { $("eval").textContent = new Function("return 'eval ok'")(); } catch { $("eval").textContent = "eval blocked"; }
try {
  const source = "postMessage('worker ok'); fetch('${origin}/data').then(() => postMessage('connected'), () => postMessage('blocked'));";
  const worker = new Worker(URL.createObjectURL(new Blob([source], { type: "text/javascript" })));
  worker.onmessage = (event) => { $(event.data === "worker ok" ? "worker" : "worker-connect").textContent = event.data; };
  worker.onerror = () => { $("worker").textContent = "worker blocked"; };
} catch { $("worker").textContent = "worker blocked"; }
</script>
</body></html>`;
}

const VIEW = "ui://counter/view";
// A view that goes elsewhere before it even loads.
const LEAVING_VIEW = "ui://counter/leaving";

/** A Streamable HTTP MCP server, answering in JSON, with one view. */
function startCounterServer(): Promise<{ origin: string; close(): void }> {
  let appsSupported = false;
  const server = http.createServer(async (req, res) => {
    const origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
    const cors = { "Access-Control-Allow-Origin": "*" };
    if (req.method === "GET" && req.url === "/sdk.js") {
      res.writeHead(200, { ...cors, "Content-Type": "text/javascript" }).end(sdk);
      return;
    }
    if (req.method === "GET" && req.url === "/data") {
      res.writeHead(200, cors).end("data");
      return;
    }
    // A page a view goes to, which tries to use the view's host, at once
    // and once loaded.
    if (req.method === "GET" && req.url === "/elsewhere") {
      res.writeHead(200, { "Content-Type": "text/html" }).end(`<p id="sent"></p><script>
const tell = (text) => parent.postMessage({ jsonrpc: "2.0", id: 1, method: "ui/update-model-context",
  params: { content: [{ type: "text", text }] } }, "*");
tell("from elsewhere at once");
addEventListener("load", () => {
  tell("from elsewhere");
  document.getElementById("sent").textContent = "sent";
});
</script>`);
      return;
    }
    if (req.method !== "POST") {
      res.writeHead(req.method === "DELETE" ? 200 : 405).end();
      return;
    }
    let body = "";
    for await (const chunk of req) body += chunk;
    const message = JSON.parse(body);
    if (message.id === undefined) {
      res.writeHead(202).end();
      return;
    }
    const text = (t: string, count: number) => ({
      content: [{ type: "text", text: t }],
      structuredContent: { count },
    });
    const { method, params } = message;
    let result: unknown;
    if (method === "initialize") {
      appsSupported = !!params.capabilities?.extensions?.["io.modelcontextprotocol/ui"];
      result = {
        protocolVersion: params.protocolVersion,
        capabilities: { tools: {}, resources: {} },
        serverInfo: { name: "counter", version: "1.0.0" },
      };
    } else if (method === "tools/list") {
      const schema = (key: string) => ({
        type: "object",
        properties: { [key]: { type: "number" } },
      });
      result = {
        tools: [
          {
            name: "show_counter",
            description: "Shows a counter.",
            inputSchema: schema("start"),
            _meta: { ui: { resourceUri: VIEW } },
          },
          {
            name: "increment",
            description: "Adds one.",
            inputSchema: schema("from"),
            _meta: { ui: { resourceUri: VIEW, visibility: ["app"] } },
          },
          {
            name: "leave",
            description: "Shows a view that leaves.",
            inputSchema: schema("unused"),
            _meta: { ui: { resourceUri: LEAVING_VIEW } },
          },
        ],
      };
    } else if (method === "tools/call" && params.name === "show_counter") {
      const start = params.arguments.start;
      result = appsSupported ? text(`Counter at ${start}`, start) : text("no MCP Apps", 0);
    } else if (method === "tools/call" && params.name === "leave") {
      result = text("left", 0);
    } else if (method === "tools/call" && params.name === "increment") {
      result = text(`Counter at ${params.arguments.from + 1}`, params.arguments.from + 1);
    } else if (method === "resources/list") {
      result = {
        resources: [{ uri: VIEW, name: "Counter", mimeType: "text/html;profile=mcp-app" }],
      };
    } else if (method === "resources/read" && params.uri === VIEW) {
      result = {
        contents: [
          {
            uri: VIEW,
            mimeType: "text/html;profile=mcp-app",
            text: counterView(origin),
            _meta: { ui: { csp: { resourceDomains: [origin] } } },
          },
        ],
      };
    } else if (method === "resources/read" && params.uri === LEAVING_VIEW) {
      result = {
        contents: [
          {
            uri: LEAVING_VIEW,
            mimeType: "text/html;profile=mcp-app",
            text: `<script>location.href = "${origin}/elsewhere";</script>`,
          },
        ],
      };
    } else {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(
        JSON.stringify({
          jsonrpc: "2.0",
          id: message.id,
          error: { code: -32601, message: method },
        }),
      );
      return;
    }
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ jsonrpc: "2.0", id: message.id, result }));
  });
  return new Promise((resolve) =>
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address() as AddressInfo;
      resolve({ origin: `http://127.0.0.1:${port}`, close: () => server.close() });
    }),
  );
}

async function addServer(request: APIRequestContext, name: string, url: string) {
  await request.delete(`/api/mcp/servers/${name}`);
  const response = await request.post("/api/mcp/servers", { data: { name, url } });
  expect(response.ok(), await response.text()).toBeTruthy();
}

async function send(page: Page, text: string) {
  await page.getByTestId("message-input").fill(text);
  await page.getByTestId("send-button").click();
}

async function agentSays(page: Page, text: string | RegExp) {
  await expect(page.locator(".message-agent").filter({ hasText: text }).last()).toBeVisible({
    timeout: 30000,
  });
}

test("an MCP tool's view renders and works with the conversation", async ({ page, request }) => {
  const counter = await startCounterServer();
  try {
    const name = `counter${test.info().workerIndex}`;
    await addServer(request, name, `${counter.origin}/mcp`);
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      `bash: ${shelley} mcp call ${name}.show_counter start=1 && ${shelley} mcp call ${name}.leave`,
    );

    // The agent learns the user sees the result.
    const messages = (await (await request.get(`/api/conversation/${conversationId}`)).json())
      .messages as { type: string; llm_data?: string }[];
    expect(messages.map((m) => m.llm_data ?? "").join("")).toContain("interactive view");

    await page.goto(`/c/${slug}`);
    const card = page.getByTestId("message-mcpapp").filter({ hasText: `${name}.show_counter` });
    const view = card.frameLocator("iframe");
    // The view that left before loading shows the page it went to, which has
    // no way to the host.
    const left = page.getByTestId("message-mcpapp").filter({ hasText: `${name}.leave` });
    await expect(left.frameLocator("iframe").locator("#sent")).toHaveText("sent");

    // The call's input and result, and the host's theme.
    await expect(view.locator("#count")).toHaveText("Counter at 1");
    await expect(view.locator("#input")).toHaveText("start=1");
    await expect(view.locator("#theme")).toHaveText("theme light");
    const background = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue("--bg-base").trim(),
    );
    await expect(view.locator("#background")).toHaveText(background);
    await expect(view.locator("#mode")).toHaveText("mode inline");
    // The view loads its script from its resourceDomains, and nothing else.
    await expect(view.locator("#connect")).toHaveText("blocked");
    await expect(view.locator("#eval")).toHaveText("eval ok");
    await expect(view.locator("#worker")).toHaveText("worker ok");
    await expect(view.locator("#worker-connect")).toHaveText("blocked");

    // An app-only tool, and context the model gets with the next message.
    await view.locator("#increment").click();
    await expect(view.locator("#count")).toHaveText("Counter at 2");
    await send(page, "app context?");
    await agentSays(page, `<view_context source="${name}.show_counter"`);
    await agentSays(page, "The counter shows 2");
    await expect(
      page.getByTestId("message-app-context").filter({ hasText: `${name}.show_counter` }),
    ).toBeVisible();
    // Taken once.
    await send(page, "app context?");
    await agentSays(page, "no app context");

    // A message from the view, which the user allows.
    const consent = page.getByTestId("mcp-app-consent");
    await view.locator("#tell").click();
    await expect(consent).toContainText(`${name}.show_counter asks to send`);
    await expect(consent).toContainText("echo: counter at 2");
    await consent.getByRole("button", { name: "Send" }).click();
    await expect(
      page.locator(".message-user").filter({ hasText: "echo: counter at 2" }),
    ).toBeVisible();
    await expect(consent).toHaveCount(0);
    // Or doesn't.
    const unasked = view
      .locator("body")
      .evaluate(() =>
        (window as unknown as { tell(text: string): Promise<unknown> })
          .tell("echo: unasked")
          .then((result) => JSON.stringify(result)),
      );
    await expect(consent).toContainText("echo: unasked");
    // Another request can't take its place.
    expect(
      await view
        .locator("body")
        .evaluate(() =>
          (window as unknown as { tell(text: string): Promise<unknown> })
            .tell("echo: swapped")
            .then(JSON.stringify, (err: Error) => err.message),
        ),
    ).toContain("waiting for the user");
    await expect(consent).toContainText("echo: unasked");
    await consent.getByRole("button", { name: "Cancel" }).click();
    expect(await unasked).toContain('"isError":true');
    await expect(consent).toHaveCount(0);

    // Fullscreen, and back.
    await view.locator("#fullscreen").click();
    const frame = card.getByTestId("mcp-app-frame");
    await expect(frame).toHaveAttribute("data-display-mode", "fullscreen");
    await expect(view.locator("#mode")).toHaveText("mode fullscreen");
    await frame.getByRole("button", { name: "Close" }).click();
    await expect(frame).toHaveAttribute("data-display-mode", "inline");
    await expect(view.locator("#mode")).toHaveText("mode inline");

    // The theme follows Shelley's.
    await page.emulateMedia({ colorScheme: "dark" });
    await expect(view.locator("#theme")).toHaveText("theme dark");
    const dark = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue("--bg-base").trim(),
    );
    expect(dark).not.toBe(background);
    await expect(view.locator("#background")).toHaveText(dark);

    // Ports the view hands on don't reach the host (and handing on its own
    // ends what it hears from the host), nor does a page the view goes to.
    await view
      .locator("body")
      .evaluate(() => (window as unknown as { handOn(): Promise<void> }).handOn());
    // What the host says next goes past the view, but tells them nothing.
    await page.emulateMedia({ colorScheme: "light" });
    const overheard = () =>
      view.locator("body").evaluate(() => (window as unknown as { overheard: string[] }).overheard);
    await expect.poll(overheard).not.toEqual([]);
    expect(await overheard()).toEqual((await overheard()).map(() => "null"));
    await view.locator("#leave").click();
    await expect(view.locator("#sent")).toHaveText("sent");
    await send(page, "app context?");
    await expect(page.locator(".message-agent").filter({ hasText: "no app context" })).toHaveCount(
      2,
      { timeout: 30000 },
    );
    await expect(page.locator(".message-user").filter({ hasText: "unasked" })).toHaveCount(0);
    await expect(page.locator(".message-agent").filter({ hasText: "elsewhere" })).toHaveCount(0);
    await expect(page.locator(".message-agent").filter({ hasText: "handed on" })).toHaveCount(0);
  } finally {
    counter.close();
  }
});

test("output_html pages are views too", async ({ page, request }) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "output-html-"));
  const file = path.join(dir, "page.html");
  fs.writeFileSync(
    file,
    `<!doctype html><html><head><title>Picker</title></head><body>
<p id="theme"></p><p id="font"></p><p id="libs"></p>
<button id="pick">Pick blue</button>
<button id="send">Send</button>
<button id="save">Save</button>
<script>
const $ = (id) => document.getElementById(id);
shelley.ready.then(() => {
  $("theme").textContent = "theme " + shelley.hostContext.theme;
  $("font").textContent = getComputedStyle(document.documentElement).fontFamily;
});
shelley.onHostContextChange((ctx) => { $("theme").textContent = "theme " + ctx.theme; });
window.__LIBS__.then((libs) => { $("libs").textContent = Object.keys(libs).join(); });
$("pick").onclick = () => shelley.setContext("The user picked blue");
$("send").onclick = () => shelley.sendMessage("echo: sent from the page");
$("save").onclick = () => shelley.download("colors.txt", "blue", "text/plain");
</script>
</body></html>`,
  );
  try {
    const { slug } = await createConversationViaAPIWithDetails(
      request,
      `output_html: ${file} with excalidraw`,
    );
    await page.goto(`/c/${slug}`);
    const view = page.frameLocator(".output-iframe-tool iframe");
    await expect(view.locator("#theme")).toHaveText("theme light");
    // Themed by default.
    const font = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue("--font-sans").trim(),
    );
    await expect(view.locator("#font")).toHaveText(font);
    // Its libraries come to it, and only to it, like the host's messages.
    await expect(view.locator("#libs")).toHaveText("excalidraw");

    await view.locator("#pick").click();
    await send(page, "app context?");
    await agentSays(page, "The user picked blue");

    const consent = page.getByTestId("mcp-app-consent");
    await view.locator("#send").click();
    await consent.getByRole("button", { name: "Send" }).click();
    await expect(
      page.locator(".message-user").filter({ hasText: "echo: sent from the page" }),
    ).toBeVisible();

    await view.locator("#save").click();
    await expect(consent).toContainText("colors.txt (4 B)");
    const download = page.waitForEvent("download");
    await consent.getByRole("button", { name: "Save" }).click();
    expect((await download).suggestedFilename()).toBe("colors.txt");

    await page.emulateMedia({ colorScheme: "dark" });
    await expect(view.locator("#theme")).toHaveText("theme dark");
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
