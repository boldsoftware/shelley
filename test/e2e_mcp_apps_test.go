package test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Ported from ui/e2e/mcp-apps.spec.ts: an MCP server whose tool has a view,
// called by the agent through `shelley mcp call`, rendered in the
// conversation, and driven through the official App SDK. Then output_html
// pages, which use the same host.

// mcpAppsSDK is the MCP Apps SDK the view imports: the
// @modelcontextprotocol/ext-apps package's app-with-deps export, from the
// UI's node_modules (pnpm install in shelley/ui puts it there).
func mcpAppsSDK(s *shelley) []byte {
	dir := filepath.Join(e2eModuleDir, "ui", "node_modules", "@modelcontextprotocol", "ext-apps")
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		s.fail("the MCP Apps SDK is missing (run pnpm install in shelley/ui): %v", err)
	}
	var pkg struct {
		Exports map[string]json.RawMessage `json:"exports"`
	}
	s.check(json.Unmarshal(b, &pkg))
	// An export is a path, or conditions mapping to one.
	var file string
	if err := json.Unmarshal(pkg.Exports["./app-with-deps"], &file); err != nil {
		var conditions map[string]string
		if err := json.Unmarshal(pkg.Exports["./app-with-deps"], &conditions); err != nil {
			s.fail("ext-apps has no ./app-with-deps export: %v", err)
		}
		file = conditions["import"]
		if file == "" {
			file = conditions["default"]
		}
	}
	if file == "" {
		s.fail("ext-apps's ./app-with-deps export names no file")
	}
	sdk, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		s.fail("the MCP Apps SDK is missing (run pnpm install in shelley/ui): %v", err)
	}
	return sdk
}

// mcpAppsCounterView is the view: the counter, what the host told it, and
// buttons that use it. ORIGIN stands for the counter server's origin.
const mcpAppsCounterView = `<!doctype html>
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
<p id="told"></p>
<button id="tell">Tell Shelley</button>
<button id="keep">Tell Shelley, keeping the focus</button>
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
import { App, applyDocumentTheme, applyHostStyleVariables } from "ORIGIN/sdk.js";
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
let told = 0;
$("keep").onpointerdown = $("keep").onmousedown = (event) => event.preventDefault();
$("tell").onclick = $("keep").onclick = () =>
  app.sendMessage({ role: "user", content: [{ type: "text", text: "echo: counter at " + count }] })
    .then((result) => { $("told").textContent = (result.isError ? "declined " : "sent ") + ++told; },
      (err) => { $("told").textContent = err.message; });
$("fullscreen").onclick = () => app.requestDisplayMode({ mode: "fullscreen" });
$("leave").onclick = () => { location.href = "ORIGIN/elsewhere"; };
// For the test to call without a click.
window.tell = (text) => app.sendMessage({ role: "user", content: [{ type: "text", text }] });
window.fullscreen = () => app.requestDisplayMode({ mode: "fullscreen" }).then((result) => result.mode);
window.grab = () => { window.focus(); $("tell").focus(); return document.hasFocus() ? "focused" : "not focused"; };
window.grabSoon = (ms) => { setTimeout(window.grab, ms); return true; };
// Not in the view's connectDomains, so the CSP blocks it.
fetch("ORIGIN/data").then(() => { $("connect").textContent = "connected"; }, () => { $("connect").textContent = "blocked"; });
// Libraries like CesiumJS eval, and start workers from blob: URLs, which
// the CSP still holds to the view's domains.
try { $("eval").textContent = new Function("return 'eval ok'")(); } catch { $("eval").textContent = "eval blocked"; }
try {
  const source = "postMessage('worker ok'); fetch('ORIGIN/data').then(() => postMessage('connected'), () => postMessage('blocked'));";
  const worker = new Worker(URL.createObjectURL(new Blob([source], { type: "text/javascript" })));
  worker.onmessage = (event) => { $(event.data === "worker ok" ? "worker" : "worker-connect").textContent = event.data; };
  worker.onerror = () => { $("worker").textContent = "worker blocked"; };
} catch { $("worker").textContent = "worker blocked"; }
</script>
</body></html>`

// mcpAppsElsewhere is a page a view goes to, which tries to use the view's
// host, at once and once loaded.
const mcpAppsElsewhere = `<p id="sent"></p><script>
const tell = (text) => parent.postMessage({ jsonrpc: "2.0", id: 1, method: "ui/update-model-context",
  params: { content: [{ type: "text", text }] } }, "*");
tell("from elsewhere at once");
addEventListener("load", () => {
  tell("from elsewhere");
  document.getElementById("sent").textContent = "sent";
});
</script>`

const (
	mcpAppsView = "ui://counter/view"
	// A view that goes elsewhere before it even loads.
	mcpAppsLeavingView = "ui://counter/leaving"
)

// mcpAppsCounterServer starts a Streamable HTTP MCP server, answering in
// JSON, with one view, and returns its origin. The browser loads the SDK and
// the page a view goes to from it too.
func mcpAppsCounterServer(s *shelley) string {
	sdk := mcpAppsSDK(s)
	var mu sync.Mutex
	appsSupported := false
	var origin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/sdk.js":
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(sdk)
			return
		case r.Method == "GET" && r.URL.Path == "/data":
			w.Header().Set("Access-Control-Allow-Origin", "*")
			io.WriteString(w, "data")
			return
		case r.Method == "GET" && r.URL.Path == "/elsewhere":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, mcpAppsElsewhere)
			return
		case r.Method == "DELETE":
			return
		case r.Method != "POST":
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
				Capabilities    struct {
					Extensions map[string]any `json:"extensions"`
				} `json:"capabilities"`
				Name      string             `json:"name"`
				Arguments map[string]float64 `json:"arguments"`
				URI       string             `json:"uri"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(message.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		text := func(t string, count float64) map[string]any {
			return map[string]any{
				"content":           []any{map[string]any{"type": "text", "text": t}},
				"structuredContent": map[string]any{"count": count},
			}
		}
		schema := func(key string) map[string]any {
			return map[string]any{"type": "object", "properties": map[string]any{key: map[string]any{"type": "number"}}}
		}
		p := message.Params
		var result any
		switch {
		case message.Method == "initialize":
			mu.Lock()
			_, appsSupported = p.Capabilities.Extensions["io.modelcontextprotocol/ui"]
			mu.Unlock()
			result = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
				"serverInfo":      map[string]any{"name": "counter", "version": "1.0.0"},
			}
		case message.Method == "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{
					"name": "show_counter", "description": "Shows a counter.", "inputSchema": schema("start"),
					"_meta": map[string]any{"ui": map[string]any{"resourceUri": mcpAppsView}},
				},
				map[string]any{
					"name": "increment", "description": "Adds one.", "inputSchema": schema("from"),
					"_meta": map[string]any{"ui": map[string]any{"resourceUri": mcpAppsView, "visibility": []string{"app"}}},
				},
				map[string]any{
					"name": "leave", "description": "Shows a view that leaves.", "inputSchema": schema("unused"),
					"_meta": map[string]any{"ui": map[string]any{"resourceUri": mcpAppsLeavingView}},
				},
			}}
		case message.Method == "tools/call" && p.Name == "show_counter":
			mu.Lock()
			ok := appsSupported
			mu.Unlock()
			start := p.Arguments["start"]
			if ok {
				result = text(fmt.Sprintf("Counter at %g", start), start)
			} else {
				result = text("no MCP Apps", 0)
			}
		case message.Method == "tools/call" && p.Name == "leave":
			result = text("left", 0)
		case message.Method == "tools/call" && p.Name == "increment":
			n := p.Arguments["from"] + 1
			result = text(fmt.Sprintf("Counter at %g", n), n)
		case message.Method == "resources/list":
			result = map[string]any{"resources": []any{
				map[string]any{"uri": mcpAppsView, "name": "Counter", "mimeType": "text/html;profile=mcp-app"},
			}}
		case message.Method == "resources/read" && p.URI == mcpAppsView:
			result = map[string]any{"contents": []any{map[string]any{
				"uri":      mcpAppsView,
				"mimeType": "text/html;profile=mcp-app",
				"text":     strings.ReplaceAll(mcpAppsCounterView, "ORIGIN", origin),
				"_meta":    map[string]any{"ui": map[string]any{"csp": map[string]any{"resourceDomains": []string{origin}}}},
			}}}
		case message.Method == "resources/read" && p.URI == mcpAppsLeavingView:
			result = map[string]any{"contents": []any{map[string]any{
				"uri":      mcpAppsLeavingView,
				"mimeType": "text/html;profile=mcp-app",
				"text":     `<script>location.href = "` + origin + `/elsewhere";</script>`,
			}}}
		default:
			writeJSON(w, map[string]any{
				"jsonrpc": "2.0", "id": message.ID,
				"error": map[string]any{"code": -32601, "message": message.Method},
			})
			return
		}
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
	}))
	origin = srv.URL
	go func() {
		<-s.ctx.Done()
		srv.Close()
	}()
	return origin
}

// mcpAppsHow is how the tests talk to the conversation and the views' host.
const mcpAppsHow = `Name elements by ARIA role and accessible name, as Playwright's getByRole does; "named X" means an exact name match. "The consent bar" is data-testid "mcp-app-consent", where the host asks the user's OK for links and downloads a view asks for. "The text box" is data-testid "message-input", and "the draft label" data-testid "view-draft", which shows above the text box while it holds a message a view asked to send. To "send T": fill data-testid "message-input" with T and click data-testid "send-button". "The agent says T": the last ".message-agent" whose text contains T should be visible (timeout 30s). `

func TestE2EMcpAppsViewWorksWithConversation(t *testing.T) {
	e2e(t, `An MCP tool's view renders and works with the conversation. The MCP server "counter" has the tool show_counter, whose view shows a counter, the app-only tool increment, and the tool leave, whose view goes to another page before it loads. The agent has run, in the conversation /c/{{SLUG}}, "shelley mcp call counter.show_counter start=1" and then "shelley mcp call counter.leave". Each call's view shows in the conversation in a card (data-testid "message-mcpapp") as an iframe titled with the call's name: "the view" is the frame "iframe[title='counter.show_counter']", and "the left view" the frame "iframe[title='counter.leave']". The view's elements are #count, #input, #theme, #background, #mode, #connect, #eval, #worker and #worker-connect, and the buttons #increment, #tell (asks the host to send "echo: counter at" and the count as the user's message, then shows in #told how that went: "sent" or "declined", and how many times it has heard back, or the error), #keep (does what #tell does, but keeps the focus from moving to it, as canvases and toolbars do), #fullscreen and #leave; its scripts define window.tell(text) (asks the host to send text as the user's message), window.fullscreen() (asks the host for fullscreen and returns the mode it got), window.grab() (takes the focus, if it can, for #tell, and says whether it has it), window.handOn() (hands every port the view holds to another one) and window.overheard (what reached the ports handed on). The page a view goes to has #sent. "The card" is the data-testid "message-mcpapp" whose text contains "counter.show_counter", and "the frame" its data-testid "mcp-app-frame". Call "agent told" says whether the agent was told the user sees the result in an interactive view ("true" or "false"). `+mcpAppsHow+`Perform the steps in order. 1. Call "agent told": it must return "true". 2. Navigate to /c/{{SLUG}}. The view that left before loading shows the page it went to: the left view's #sent should have exactly the text "sent". 3. The call's input and result, and the host's theme: the view's #count should have exactly the text "Counter at 1", its #input "start=1" and its #theme "theme light". Eval, in the page, getComputedStyle(document.documentElement).getPropertyValue("--bg-base").trim(), saving it as BG: the view's #background should have exactly the text {{BG}}. Its #mode should have exactly the text "mode inline". The view loads its script from its resourceDomains, and nothing else: its #connect should have exactly the text "blocked", #eval "eval ok", #worker "worker ok" and #worker-connect "blocked". 4. An app-only tool, and context the model gets with the next message: click the view's #increment; its #count should have exactly the text "Counter at 2". Send "app context?". The agent says '<view_context source="counter.show_counter"', and the agent says "The counter shows 2". The data-testid "message-app-context" whose text contains "counter.show_counter" should be visible. 5. Taken once: send "app context?"; the agent says "no app context". 6. A message from the view waits in the text box for the user's Send: click the view's #tell. The text box should have the value exactly "echo: counter at 2", and the draft label exactly the text "From counter.show_counter". Click data-testid "send-button". The ".message-user" whose text contains "echo: counter at 2" should be visible, the view's #told should have exactly the text "sent 1", and there should be no draft label (count 0). 7. A newer one takes the place of one the user left as it was: click the view's #tell twice. The view's #told should have exactly the text "declined 2", and the text box the value exactly "echo: counter at 2". 8. And joins what the user wrote: fill the text box with "Sure.". Click the view's #tell. The view's #told should have exactly the text "declined 3". Eval, in the page, document.querySelector('[data-testid="message-input"]').value === "Sure.\n\necho: counter at 2": it must return true. 9. Clearing the text box turns it down: fill the text box with "". The view's #told should have exactly the text "declined 4", and there should be no draft label (count 0). 10. Not without a click in the view, nor as a command: click the text box (a click in Shelley's page ends what a click in the view allowed), then eval, in the view, window.tell("echo: unasked").then(JSON.stringify, (err) => err.message): it must return text containing "needs a click". Eval, in the view, window.tell("!echo unasked").then(JSON.stringify, (err) => err.message): it must return text containing "not commands". Typing in the text box isn't a click in the view, even if the view then takes the screen: click the text box and press the keys "S", "u", "r", "e" one by one. Eval, in the view, window.fullscreen(): it must return "inline". Eval, in the view, window.grab(): it must return "focused". Nor do the keys the user goes on typing, which go to the view now: press the key "x". Eval, in the view, window.tell("echo: unasked").then(JSON.stringify, (err) => err.message): it must return text containing "needs a click". Nor a click such a key makes: press the key "Enter", which clicks #tell. The view's #told should contain the text "needs a click". The text box should have the value exactly "Sure". Fill the text box with "". Nor into a command the user is writing: fill the text box with "!ls". Click the view's #tell. The view's #told should contain the text "busy", and the text box have the value exactly "!ls". Fill the text box with "". Keys count where the user put the focus: click the view's #count. Eval, in the view, window.tell("first").then(JSON.stringify, (err) => err.message), without waiting for it to settle (it settles later), and the text box should have the value exactly "first". Eval, in the view, document.getElementById("tell").focus(), then press the key "Enter". The text box should have the value exactly "echo: counter at 2". Fill the text box with ""; the view's #told should have exactly the text "declined 5". And where the user tabbed: click the text box, then eval, in the page, (() => { const frame = document.querySelector("iframe[title='counter.show_counter']"); const all = [...document.querySelectorAll("button, a[href], input, textarea, select, iframe, [tabindex]")].filter((e) => e.tabIndex >= 0 && e.offsetParent); all[all.indexOf(frame) - 1].focus(); return true; })(): it must return true. Press the key "Tab"; eval, in the page, document.activeElement.title: it must return "counter.show_counter". Press the keys "Tab" and "Enter" one by one. The text box should have the value exactly "echo: counter at 2". Fill the text box with ""; the view's #told should have exactly the text "declined 6". A Tab elsewhere in Shelley's page hands nothing to a view that takes the focus right after: click the text box; eval, in the view, window.grabSoon(150): it must return true; at once press the key "Tab"; then, in a retry, eval, in the page, document.activeElement.title: it must become "counter.show_counter". Press the key "x". Eval, in the view, window.tell('echo: unasked').then(JSON.stringify, (err) => err.message): it must contain "needs a click". The text box should have the value exactly "". And a tap: touch start on the view's #tell, then touch end. The text box should have the value exactly "echo: counter at 2". Fill the text box with ""; the view's #told should have exactly the text "declined 7". And a click that doesn't move the focus: click the text box, then click the view's #keep. The text box should have the value exactly "echo: counter at 2". Fill the text box with ""; the view's #told should have exactly the text "declined 8". 11. Fullscreen, and back: click the view's #fullscreen. The frame should have the attribute data-display-mode exactly "fullscreen", and the view's #mode exactly the text "mode fullscreen". Click the frame's button named "Close". The frame should have the attribute data-display-mode exactly "inline", and the view's #mode exactly the text "mode inline". 12. A message goes back inline to the text box: click the view's #fullscreen; the frame should have the attribute data-display-mode exactly "fullscreen". Click the view's #tell. The frame should have the attribute data-display-mode exactly "inline", the view's #mode exactly the text "mode inline", and the text box the value exactly "echo: counter at 2". Fill the text box with ""; the view's #told should have exactly the text "declined 9". A message the user hasn't sent isn't kept as their draft: click the view's #tell; the text box should have the value exactly "echo: counter at 2". Reload the page. The view's #count should have exactly the text "Counter at 1", the text box the value exactly "", and there should be no draft label (count 0). 13. The theme follows Shelley's: emulate prefers-color-scheme: dark. The view's #theme should have exactly the text "theme dark". Eval, in the page, getComputedStyle(document.documentElement).getPropertyValue("--bg-base").trim() again, saving it as DARK. Eval "{{DARK}}" === "{{BG}}": it must return false. The view's #background should have exactly the text {{DARK}}. 14. Ports the view hands on don't reach the host (and handing on its own ends what it hears from the host): eval, in the view, window.handOn(). What the host says next goes past the view, but tells them nothing: emulate prefers-color-scheme: light, then eval, in the view, window.overheard.length ? JSON.stringify(window.overheard.filter((m) => m !== "null")) : "none yet" until it returns "[]". 15. Nor does a page the view goes to: click the view's #leave; the view's #sent should have exactly the text "sent". Send "app context?". There should be exactly 2 ".message-agent" whose text contains "no app context" (timeout 30s). There should be no ".message-user" whose text contains "unasked" (count 0), no ".message-agent" whose text contains "elsewhere" (count 0), and no ".message-agent" whose text contains "handed on" (count 0).`, func(s *shelley) {
		const name = "counter"
		origin := mcpAppsCounterServer(s)
		s.api("POST", "/api/mcp/servers", map[string]any{"name": name, "url": origin + "/mcp"}, nil)
		c := s.conversation(fmt.Sprintf("bash: %[1]s mcp call %[2]s.show_counter start=1 && %[1]s mcp call %[2]s.leave", e2eBin, name), "")
		s.v("SLUG", c.Slug)
		s.call("agent told", func() string {
			var got struct {
				Messages []struct {
					LLMData string `json:"llm_data"`
				} `json:"messages"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &got)
			var all strings.Builder
			for _, m := range got.Messages {
				all.WriteString(m.LLMData)
			}
			return fmt.Sprint(strings.Contains(all.String(), "interactive view"))
		})
	})
}

// mcpAppsOutputHTMLPage is the page output_html shows.
const mcpAppsOutputHTMLPage = `<!doctype html><html><head><title>Picker</title></head><body>
<p id="theme"></p><p id="font"></p><p id="libs"></p><p id="sent"></p><p id="saved"></p>
<button id="pick">Pick blue</button>
<button id="send">Send</button><button id="seal">Seal</button>
<button id="save">Save</button>
<script>
const $ = (id) => document.getElementById(id);
shelley.ready.then(() => {
  $("theme").textContent = "theme " + shelley.hostContext.theme;
  $("font").textContent = getComputedStyle(document.documentElement).fontFamily;
});
shelley.onHostContextChange((ctx) => { $("theme").textContent = "theme " + ctx.theme; });
window.__LIBS__?.then((libs) => { $("libs").textContent = Object.keys(libs).join(); });
$("pick").onclick = () => shelley.setContext("The user picked blue");
const report = (id, done) => (promise) =>
  promise.then(() => { $(id).textContent = done; }, (err) => { $(id).textContent = err.message; });
$("send").onclick = () => report("sent", "sent")(shelley.sendMessage("echo: sent from the page"));
$("seal").onclick = () => report("sent", "sent")(shelley.sendMessage("echo: fw_ViewMadeToken12345678 from the page"));
$("save").onclick = () => report("saved", "saved")(shelley.download("colors.txt", "blue", "text/plain"));
</script>
</body></html>`

func TestE2EMcpAppsOutputHtmlPagesAreViews(t *testing.T) {
	e2e(t, `output_html pages are views too. The agent has shown, in the conversation /c/{{SLUG}}, a page with output_html and the excalidraw library: "the view" is the frame ".output-iframe-tool iframe". The page has #theme, #font and #libs, and the buttons #pick (sets the context for the model to "The user picked blue"), #send (asks to send "echo: sent from the page" as the user's message, then shows how that went in #sent) and #save (asks to download colors.txt, holding "blue", then shows how that went in #saved). `+mcpAppsHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}. The view's #theme should have exactly the text "theme light". 2. Themed by default: eval, in the page, getComputedStyle(document.documentElement).getPropertyValue("--font-sans").trim(), saving it as FONT: the view's #font should have exactly the text {{FONT}}. 3. Its libraries come to it, and only to it, like the host's messages: the view's #libs should have exactly the text "excalidraw". 4. Click the view's #pick. Send "app context?": the agent says "The user picked blue". 5. Click the view's #send. The text box should have the value exactly "echo: sent from the page", and the draft label exactly the text "From Predictable view". Click data-testid "send-button". The ".message-user" whose text contains "echo: sent from the page" should be visible, and the view's #sent should have exactly the text "sent". 6. Click the view's #save. The consent bar should contain the text "colors.txt (4 B)", and no download should have begun yet. One request at a time: click the view's #save again; the view's #saved should contain the text "waiting for the user", and the consent bar should still be visible. Click the consent bar's button named "Save": a download whose suggested file name is exactly "colors.txt" should begin, and the view's #saved should have exactly the text "saved". 7. Emulate prefers-color-scheme: dark. The view's #theme should have exactly the text "theme dark".`, func(s *shelley) {
		page := filepath.Join(s.dir, "output-html", "page.html")
		s.write(page, mcpAppsOutputHTMLPage)
		s.v("SLUG", s.conversation("output_html: "+page+" with excalidraw", "").Slug)
	})
}

func TestE2EMcpAppsRecordingLeavesViewMessage(t *testing.T) {
	e2e(t, `A recording, which goes out without the user's Send, takes back a view's message the user left as it was. The agent has shown, in the conversation /c/{{SLUG}}, a page with output_html: "the view" is the frame ".output-iframe-tool iframe". Its button #send asks to send "echo: sent from the page" as the user's message, and #seal "echo: fw_ViewMadeToken12345678 from the page" (which looks like it holds an API key); both then show how that went in #sent. `+mcpAppsHow+recMocks+`The voice button is [data-testid="voice-button"], the recording panel [data-testid="recording-panel"], and its cancel button [data-testid="recording-cancel-button"]. Perform the steps in order. 1. Navigate to /c/{{SLUG}}. Fill the text box with "Mine.". 2. Click the view's #send. Eval, in the page, document.querySelector('[data-testid="message-input"]').value === "Mine.\n\necho: sent from the page": it must return true. The draft label should be visible. 3. Click the voice button (a click step on its selector, not an eval). The recording panel should be visible, and window.__recordingMock.recorderStarts should become 1. [data-testid="recording-preserved-text"] should have exactly the text "Mine.", and the view's #sent exactly the text "The user declined.". 4. Click the cancel button. No recording panel should remain (count 0). The text box should have the value exactly "Mine.", and there should be no draft label (count 0). 5. Once the user edits, the text is theirs. Eval, in the view, (document.getElementById("sent").textContent = "", true): it must return true. Click the view's #send; the text box should have the value exactly "Mine.\n\necho: sent from the page". Eval, in the page, (() => { const box = document.querySelector('[data-testid="message-input"]'); box.focus(); box.setSelectionRange(0, 0); return true; })(): it must return true. Type "Oh. " (into the focused text box, at the start). Eval, in the page, document.querySelector('[data-testid="message-input"]').value === "Oh. Mine.\n\necho: sent from the page": it must return true. The draft label should be visible. 6. Click the voice button. window.__recordingMock.recorderStarts should become 2. [data-testid="recording-preserved-text"] should contain the text "echo: sent from the page", and the view's #sent should have exactly the text "The user declined.". 7. Click the cancel button. Eval, in the page, document.querySelector('[data-testid="message-input"]').value === "Oh. Mine.\n\necho: sent from the page": it must return true. There should be no draft label (count 0). 8. Nothing but the user changes the view's text, not even sealing what looks like a secret in it. Fill the text box with "Mine.". Eval, in the view, (document.getElementById("sent").textContent = "", true): it must return true. Click the view's #seal. Eval, in the page, document.querySelector('[data-testid="message-input"]').value === "Mine.\n\necho: fw_ViewMadeToken12345678 from the page": it must return true. Click the text box (a click step on its selector), then eval, in the page, (document.querySelector('[data-testid="message-input"]').blur(), true): it must return true. Eval, in the page, document.querySelector('[data-testid="message-input"]').value === "Mine.\n\necho: fw_ViewMadeToken12345678 from the page": it must return true. 9. Click the voice button. window.__recordingMock.recorderStarts should become 3. [data-testid="recording-preserved-text"] should have exactly the text "Mine.", and the view's #sent exactly the text "The user declined.". 10. Click the cancel button. The text box should have the value exactly "Mine.", and there should be no draft label (count 0). 11. A quote the user adds is theirs, and goes ahead of the view's text, which stays the view's. Eval, in the view, (document.getElementById("sent").textContent = "", true): it must return true. Click the view's #send. Eval, in the page, document.querySelector('[data-testid="message-input"]').value === "Mine.\n\necho: sent from the page": it must return true. Eval, in the page, (() => { const all = document.querySelectorAll('[data-testid="message"][data-commentable="true"]'); all[all.length - 1].setAttribute('data-quote-me', ''); return window.selectTextIn('[data-quote-me]', 'Done'); })(): it must return "selected". The comment button ([data-testid="message-selection-comment-btn"]) should become visible; tap it: a touch step "start" on it immediately followed by a touch step "end". Eval, in the page, document.querySelector('[data-testid="message-input"]').value === "Mine.\n\n> Done\n\necho: sent from the page": it must return true. The draft label should be visible. 12. Click the voice button. window.__recordingMock.recorderStarts should become 4. [data-testid="recording-preserved-text"] should contain the text "> Done" and must not contain the text "sent from the page", and the view's #sent should have exactly the text "The user declined.". 13. Click the cancel button. Eval, in the page, document.querySelector('[data-testid="message-input"]').value.trim() === "Mine.\n\n> Done": it must return true. There should be no draft label (count 0).`, func(s *shelley) {
		recording(s)
		s.initScript(selectTextJS)
		page := filepath.Join(s.dir, "output-html", "page.html")
		s.write(page, mcpAppsOutputHTMLPage)
		s.v("SLUG", s.conversation("output_html: "+page, "").Slug)
	})
}

func TestE2EMcpAppsCommandLeavesViewMessage(t *testing.T) {
	e2e(t, `A command in the text box doesn't take a view's message along. The agent has shown, in the conversation /c/{{SLUG}}, a page with output_html: "the view" is the frame ".output-iframe-tool iframe". Its button #send asks to send "echo: sent from the page" as the user's message, then shows how that went in #sent. The conversation's directory holds t.sh, which clears the screen, prints "!echo from-terminal" and "menu-ready", then waits reading input. `+mcpAppsHow+termMenu+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}. Fill the text box with "!bash t.sh" and click [data-testid="send-button"]. ".terminal-panel-tab-running" should be visible. 2. Retry the following as one block until it passes (timeout 15s): fill the text box with ""; choose the terminal menu item "Insert visible screen into input"; eval, in the page, /menu-ready/.test(document.querySelector('[data-testid="message-input"]').value): it must return true. 3. Fill the text box with "". Click the view's #send. Eval, in the page, document.querySelector('[data-testid="message-input"]').value === "echo: sent from the page": it must return true. The draft label should be visible. 4. Choose the terminal menu item "Insert visible screen into input". Eval, in the page, (() => { const value = document.querySelector('[data-testid="message-input"]').value; return value.trim().startsWith("!echo from-terminal") && !value.includes("sent from the page"); })(): it must return true. There should be no draft label (count 0), and the view's #sent should have exactly the text "The user declined.".`, func(s *shelley) {
		page := filepath.Join(s.dir, "output-html", "page.html")
		s.write(page, mcpAppsOutputHTMLPage)
		c, _ := s.terminalConversation("view-command", "output_html: "+page, "", map[string]string{
			"t.sh": "printf '\\033[H\\033[2J'\necho '!echo from-terminal'\necho menu-ready\nexec cat\n",
		})
		s.v("SLUG", c.Slug)
	})
}
