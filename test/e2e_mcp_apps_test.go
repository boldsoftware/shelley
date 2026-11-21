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
$("tell").onclick = () =>
  app.sendMessage({ role: "user", content: [{ type: "text", text: "echo: counter at " + count }] });
$("fullscreen").onclick = () => app.requestDisplayMode({ mode: "fullscreen" });
$("leave").onclick = () => { location.href = "ORIGIN/elsewhere"; };
// For the test to call without a click.
window.tell = (text) => app.sendMessage({ role: "user", content: [{ type: "text", text }] });
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
const mcpAppsHow = `Name elements by ARIA role and accessible name, as Playwright's getByRole does; "named X" means an exact name match. "The consent bar" is data-testid "mcp-app-consent", where the host asks the user's OK for what a view asks to do. To "send T": fill data-testid "message-input" with T and click data-testid "send-button". "The agent says T": the last ".message-agent" whose text contains T should be visible (timeout 30s). `

func TestE2EMcpAppsViewWorksWithConversation(t *testing.T) {
	e2e(t, `An MCP tool's view renders and works with the conversation. The MCP server "counter" has the tool show_counter, whose view shows a counter, the app-only tool increment, and the tool leave, whose view goes to another page before it loads. The agent has run, in the conversation /c/{{SLUG}}, "shelley mcp call counter.show_counter start=1" and then "shelley mcp call counter.leave". Each call's view shows in the conversation in a card (data-testid "message-mcpapp") as an iframe titled with the call's name: "the view" is the frame "iframe[title='counter.show_counter']", and "the left view" the frame "iframe[title='counter.leave']". The view's elements are #count, #input, #theme, #background, #mode, #connect, #eval, #worker and #worker-connect, and the buttons #increment, #tell, #fullscreen and #leave; its scripts define window.tell(text) (asks the host to send text as the user's message), window.handOn() (hands every port the view holds to another one) and window.overheard (what reached the ports handed on). The page a view goes to has #sent. "The card" is the data-testid "message-mcpapp" whose text contains "counter.show_counter", and "the frame" its data-testid "mcp-app-frame". Call "agent told" says whether the agent was told the user sees the result in an interactive view ("true" or "false"). `+mcpAppsHow+`Perform the steps in order. 1. Call "agent told": it must return "true". 2. Navigate to /c/{{SLUG}}. The view that left before loading shows the page it went to: the left view's #sent should have exactly the text "sent". 3. The call's input and result, and the host's theme: the view's #count should have exactly the text "Counter at 1", its #input "start=1" and its #theme "theme light". Eval, in the page, getComputedStyle(document.documentElement).getPropertyValue("--bg-base").trim(), saving it as BG: the view's #background should have exactly the text {{BG}}. Its #mode should have exactly the text "mode inline". The view loads its script from its resourceDomains, and nothing else: its #connect should have exactly the text "blocked", #eval "eval ok", #worker "worker ok" and #worker-connect "blocked". 4. An app-only tool, and context the model gets with the next message: click the view's #increment; its #count should have exactly the text "Counter at 2". Send "app context?". The agent says '<view_context source="counter.show_counter"', and the agent says "The counter shows 2". The data-testid "message-app-context" whose text contains "counter.show_counter" should be visible. 5. Taken once: send "app context?"; the agent says "no app context". 6. A message from the view, which the user allows: click the view's #tell. The consent bar should contain the text "counter.show_counter asks to send" and the text "echo: counter at 2". Click the consent bar's button named "Send". The ".message-user" whose text contains "echo: counter at 2" should be visible, and there should be no consent bar (count 0). 7. Or doesn't: eval, in the view, window.unasked = window.tell("echo: unasked").then((result) => JSON.stringify(result)); "asked". The consent bar should contain the text "echo: unasked". 8. Another request can't take its place: eval, in the view, window.swapped = window.tell("echo: swapped").then(JSON.stringify, (err) => err.message); "asked". Then eval, in the view, window.swapped.then((t) => t.includes("waiting for the user") ? "refused" : t) until it returns "refused". The consent bar should still contain the text "echo: unasked". 9. Click the consent bar's button named "Cancel". Eval, in the view, window.unasked.then((t) => t.includes('"isError":true') ? "cancelled" : t) until it returns "cancelled". There should be no consent bar (count 0). 10. Fullscreen, and back: click the view's #fullscreen. The frame should have the attribute data-display-mode exactly "fullscreen", and the view's #mode exactly the text "mode fullscreen". Click the frame's button named "Close". The frame should have the attribute data-display-mode exactly "inline", and the view's #mode exactly the text "mode inline". 11. The theme follows Shelley's: emulate prefers-color-scheme: dark. The view's #theme should have exactly the text "theme dark". Eval, in the page, getComputedStyle(document.documentElement).getPropertyValue("--bg-base").trim() again, saving it as DARK. Eval "{{DARK}}" === "{{BG}}": it must return false. The view's #background should have exactly the text {{DARK}}. 12. Ports the view hands on don't reach the host (and handing on its own ends what it hears from the host): eval, in the view, window.handOn(). What the host says next goes past the view, but tells them nothing: emulate prefers-color-scheme: light, then eval, in the view, window.overheard.length ? JSON.stringify(window.overheard.filter((m) => m !== "null")) : "none yet" until it returns "[]". 13. Nor does a page the view goes to: click the view's #leave; the view's #sent should have exactly the text "sent". Send "app context?". There should be exactly 2 ".message-agent" whose text contains "no app context" (timeout 30s). There should be no ".message-user" whose text contains "unasked" (count 0), no ".message-agent" whose text contains "elsewhere" (count 0), and no ".message-agent" whose text contains "handed on" (count 0).`, func(s *shelley) {
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
</body></html>`

func TestE2EMcpAppsOutputHtmlPagesAreViews(t *testing.T) {
	e2e(t, `output_html pages are views too. The agent has shown, in the conversation /c/{{SLUG}}, a page with output_html and the excalidraw library: "the view" is the frame ".output-iframe-tool iframe". The page has #theme, #font and #libs, and the buttons #pick (sets the context for the model to "The user picked blue"), #send (asks to send "echo: sent from the page" as the user's message) and #save (asks to download colors.txt, holding "blue"). `+mcpAppsHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}. The view's #theme should have exactly the text "theme light". 2. Themed by default: eval, in the page, getComputedStyle(document.documentElement).getPropertyValue("--font-sans").trim(), saving it as FONT: the view's #font should have exactly the text {{FONT}}. 3. Its libraries come to it, and only to it, like the host's messages: the view's #libs should have exactly the text "excalidraw". 4. Click the view's #pick. Send "app context?": the agent says "The user picked blue". 5. Click the view's #send, then the consent bar's button named "Send". The ".message-user" whose text contains "echo: sent from the page" should be visible. 6. Click the view's #save. The consent bar should contain the text "colors.txt (4 B)", and no download should have begun yet. Click the consent bar's button named "Save": a download whose suggested file name is exactly "colors.txt" should begin. 7. Emulate prefers-color-scheme: dark. The view's #theme should have exactly the text "theme dark".`, func(s *shelley) {
		page := filepath.Join(s.dir, "output-html", "page.html")
		s.write(page, mcpAppsOutputHTMLPage)
		s.v("SLUG", s.conversation("output_html: "+page+" with excalidraw", "").Slug)
	})
}
