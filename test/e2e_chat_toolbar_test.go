package test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/chat-toolbar.spec.ts.

// The header's workspace toolbar (Diffs, Git Graph, Terminal and the VM
// link) stays compact at every width: phones move it into the overflow
// menu's icon row. Links open their pages in popups.

var chatToolbarViewports = []struct{ width, height int }{
	{320, 568},
	{393, 851},
	{768, 1024},
	{1280, 800},
}

// chatToolbarJS gives pages window.toolbarCounts(), window.toolbarTitles(),
// window.toolbarLayout(W), window.menuLayout(W, H) and window.menuRow(name)
// to check the header and its overflow menu as a whole.
const chatToolbarJS = `(() => {
  const header = () => document.querySelector('.header');
  const box = e => e.getBoundingClientRect();
  // Playwright's :visible: a non-empty box, and not visibility: hidden.
  const visible = e => { const r = box(e); return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== 'hidden'; };
  const named = (root, sel, label) => {
    const m = [...root.querySelectorAll(sel)].filter(e => e.getAttribute('aria-label') === label);
    return m.length === 1 ? m[0] : null;
  };
  // The toolbar's buttons and the header's links, hidden ones included.
  window.toolbarCounts = () => {
    const h = header();
    if (!h) return 'no .header';
    const toolbar = h.querySelector('.chat-workspace-actions');
    if (!toolbar) return 'no .chat-workspace-actions';
    return JSON.stringify({
      buttons: toolbar.querySelectorAll('button, [role="button"]').length,
      links: h.querySelectorAll('a[href], [role="link"]').length,
    });
  };
  // Each toolbar button's title names its shortcut: "Name (shortcut)".
  window.toolbarTitles = () => {
    const h = header();
    if (!h) return 'no .header';
    for (const label of ['Diffs', 'Git Graph', 'Terminal']) {
      const b = named(h, '.chat-workspace-actions button', label);
      if (!b) return 'no single ' + label + ' button';
      if (!/.+\(.+\)/.test(b.title)) return label + ' title is ' + JSON.stringify(b.title);
    }
    return 'ok';
  };
  // The title keeps its room; the controls are in view, in order, apart and
  // inside the width W; on phones (W < 768) every visible control is big
  // enough to tap.
  window.toolbarLayout = (W) => {
    const h = header();
    if (!h) return 'no .header';
    const mobile = W < 768;
    const c = {
      diffs: named(h, '.chat-workspace-actions button', 'Diffs'),
      gitGraph: named(h, '.chat-workspace-actions button', 'Git Graph'),
      terminal: named(h, '.chat-workspace-actions button', 'Terminal'),
      vm: named(h, 'a', 'vm.example'),
      call: h.querySelector('[data-testid="live-call-header"]'),
      newConversation: named(h, 'button', 'New Conversation'),
      more: named(h, 'button', 'More options'),
      divider: h.querySelector('.chat-workspace-divider'),
      title: h.querySelector('.header-title'),
    };
    const missing = Object.keys(c).filter(k => !c[k]);
    if (missing.length) return 'missing ' + missing.join(', ');
    const titleWidth = box(c.title).width;
    if (titleWidth < (mobile ? 96 : 32)) return 'the title is ' + titleWidth + 'px wide';
    const inView = e => { const r = box(e); return visible(e) && r.right > 0 && r.bottom > 0 && r.left < innerWidth && r.top < innerHeight; };
    let previousRight = 0;
    const order = mobile ? ['newConversation', 'call', 'more'] : ['diffs', 'gitGraph', 'terminal', 'vm', 'call', 'newConversation', 'more'];
    for (const k of order) {
      if (!inView(c[k])) return k + ' is not in the viewport';
      const r = box(c[k]);
      if (r.left < previousRight) return k + ' starts at ' + r.left + ', before ' + previousRight;
      if (r.right > W) return k + ' ends at ' + r.right;
      previousRight = r.right;
    }
    if (!mobile) {
      const d = box(c.divider);
      if (d.left < box(c.terminal).right || d.right > box(c.vm).left) return 'the divider is not between Terminal and the VM link';
    }
    for (const e of h.querySelectorAll('button, a')) {
      if (!visible(e)) continue;
      const r = box(e);
      const name = e.getAttribute('aria-label') || e.textContent.trim();
      if (r.left < 0 || r.right > W) return name + ' spans ' + r.left + '..' + r.right;
      const min = W < 360 ? 32 : 36;
      if (mobile && (r.width < min || r.height < min)) return name + ' is ' + r.width + 'x' + r.height;
    }
    return 'ok';
  };
  // The overflow menu fits in the W x H viewport.
  window.menuLayout = (W, H) => {
    const m = document.querySelector('.chat-overflow-popover');
    if (!m) return 'no menu';
    const r = box(m);
    if (r.left < 0 || r.top < 0 || r.right > W || r.bottom > H) return 'the menu spans ' + [r.left, r.top, r.right, r.bottom].join(',');
    return 'ok';
  };
  // The single menu row containing name: "hidden", or whether its shortcut
  // shows: "shortcut visible" or "shortcut hidden".
  window.menuRow = (name) => {
    const rows = [...document.querySelectorAll('.chat-overflow-popover .overflow-menu-item')].filter(e => e.textContent.includes(name));
    if (rows.length !== 1) return rows.length + ' rows';
    if (!visible(rows[0])) return 'hidden';
    const s = rows[0].querySelector('.overflow-menu-shortcut');
    return s && visible(s) ? 'shortcut visible' : 'shortcut hidden';
  };
})();`

const chatToolbarSetup = `"The header" is ".header"; "the toolbar" is ".header .chat-workspace-actions"; "the menu" is ".chat-overflow-popover"; "More options" is the header's button named exactly "More options". "The VM link" is the header's link a[aria-label="vm.example"]. Names of buttons are accessible names: use role and name. "Hidden" means hidden or absent. Links open in a popup (tab "popup"); "it opens P" means: tab "popup" should come to have exactly the URL P (assert_url with value P), and show an element whose whole text is exactly "Local link fixture"; then close tab "popup". `

// chatToolbarLinks has the server present links as on a VM, each answered
// with a fixture page.
func chatToolbarLinks(s *shelley, includeVM bool) []serverLink {
	links := s.serverLinks(includeVM)
	for _, origin := range []string{"https://vm.example", "https://exe.example", "https://docs.example"} {
		s.host(origin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<p>Local link fixture</p>")
		}))
	}
	return links
}

// chatToolbarOpen is how a step opens a workspace tool: phones keep the
// tools in the overflow menu.
func chatToolbarOpen(mobile bool, tool string) string {
	if mobile {
		return `click More options, then click the visible button in the menu whose name contains "` + tool + `"`
	}
	if tool == "Diffs" || tool == "Git Graph" || tool == "Terminal" {
		return `click the toolbar's button named exactly "` + tool + `"`
	}
	return `click More options, then click the button in the menu whose name contains "` + tool + `"`
}

func TestE2EChatToolbarCompact320(t *testing.T)  { chatToolbarCompact(t, useful(0), 0) }
func TestE2EChatToolbarCompact393(t *testing.T)  { chatToolbarCompact(t, useful(0), 1) }
func TestE2EChatToolbarCompact768(t *testing.T)  { chatToolbarCompact(t, useful(0), 2) }
func TestE2EChatToolbarCompact1280(t *testing.T) { chatToolbarCompact(t, useful(0), 3) }

func chatToolbarCompact(t *testing.T, useful lazycue.Useful, i int) {
	W, H := chatToolbarViewports[i].width, chatToolbarViewports[i].height
	mobile := W < 768
	var d strings.Builder
	n := 0
	step := func(format string, args ...any) {
		n++
		fmt.Fprintf(&d, "%d. %s ", n, fmt.Sprintf(format, args...))
	}
	fmt.Fprintf(&d, "%sThe compact workspace toolbar and overflow menu work at %dpx (%s). Perform the steps in order. ", chatToolbarSetup, W, map[bool]string{true: "a phone: the toolbar moves into the menu", false: "a desktop width"}[mobile])
	step(`Navigate to /c/{{SLUG}} and wait for the header.`)
	step(`The header's [data-testid="live-call-header"] should be enabled, and its background-color "rgba(0, 0, 0, 0)" (getComputedStyle, exactly).`)
	step(`Eval window.toolbarCounts() with expect "{\"buttons\":3,\"links\":1}". There should be exactly one ".header .chat-workspace-divider", and it should be %s.`, map[bool]string{true: "hidden", false: "visible"}[mobile])
	step(`The header should have no buttons named exactly "Command menu", "Edit File…" or "MCP Servers" (count 0 each).`)
	if !mobile {
		step(`Eval window.toolbarTitles() with expect "ok".`)
	}
	step(`The VM link should have exactly these attributes: href "https://vm.example/website", target "_blank", rel "noopener noreferrer", aria-label "vm.example" and title "vm.example".`)
	if mobile {
		step(`The toolbar's buttons named "Diffs", "Git Graph" and "Terminal", and the VM link, should be hidden. The header's [data-testid="live-call-header"] should have aria-label exactly "Start voice conversation".`)
	}
	step(`The header's button named exactly "New Conversation" should be visible. Eval window.toolbarLayout(%d) with expect "ok".`, W)
	if !mobile {
		step(`Click the VM link; it opens https://vm.example/website, and before closing it, eval window.opener === null in tab "popup" with expect "true".`)
	}
	step(`Click More options. The menu should be visible; eval window.menuLayout(%d, %d) with expect "ok". In the menu, buttons whose names contain "Command menu", "Edit File…" and "MCP Servers" should each be visible.`, W, H)
	if mobile {
		step(`The menu should have no button whose name contains "New Conversation" (count 0). In the menu's icon row ".overflow-menu-toolbar", the buttons named exactly "Diffs", "Git Graph", "Terminal", "Edit File…" and "Command menu", and the button whose name contains "vm.example", should be visible.`)
		step(`Eval window.menuRow("Command menu") and window.menuRow("Edit File…"), each with expect "hidden", and window.menuRow("Archive Conversation") with expect "shortcut hidden".`)
	} else {
		step(`The menu should have no button whose name contains "Diffs", "Git Graph", "Terminal", "vm.example" or "New Conversation" (count 0 each). ".overflow-menu-toolbar" should be hidden.`)
		step(`Eval window.menuRow("Command menu"), window.menuRow("Edit File…") and window.menuRow("Archive Conversation"), each with expect "shortcut visible".`)
	}
	step(`Scroll the menu's "Change Language" row into view: eval [...document.querySelectorAll('.chat-overflow-popover .overflow-menu-item')].find(b => b.textContent.includes('Change Language')).scrollIntoView({block: 'center'}). The menu's button whose name contains "Change Language" should then be in the viewport. Press Escape.`)
	links := []string{`"Manage on exe.dev" (it opens https://exe.example/vm/vm)`, `"vm.example" (it opens https://vm.example/website)`, `"Documentation" (it opens https://docs.example/documentation)`}
	if !mobile {
		links = []string{links[0], links[2]}
	}
	step(`For each link name in turn, %s: click More options; click the menu's button named exactly that name; it opens its page; the menu should then be hidden.`, strings.Join(links, ", then "))
	step(`Open the command menu: %s. ".command-palette-input" should be visible; fill it with "notification"; the first ".command-palette-item" should be visible. Press Escape; the input should become hidden.`, chatToolbarOpen(mobile, "Command menu"))
	step(`Click More options, then the menu's button named exactly "MCP Servers". The dialog named exactly "MCP Servers" (role dialog) should be visible, and the menu hidden. Click the dialog's button named exactly "Close modal"; the dialog should become hidden.`)
	if W == 1280 {
		step(`Fill the message input (data-testid "message-input") with "!printf toolbar-background" and click the send button (data-testid "send-button"). ".terminal-panel-tab[title=\"printf toolbar-background\"]" should become visible.`)
	}
	step(`Open the terminal: %s. ".terminal-panel" should be visible. "The active terminal" is ".terminal-panel-content [data-terminal-id][style*=\"display: block\"]"; inside it, the textbox named exactly "Terminal input" should be visible. Remember it: eval (window.__terminal = document.querySelector('.terminal-panel-content [data-terminal-id][style*="display: block"]')?.getAttribute('data-terminal-id')) ? "ok" : "none" with expect "ok". Click the button named exactly "Close active terminal". Eval document.querySelectorAll('[data-terminal-id="' + window.__terminal + '"]').length with expect "0".`, chatToolbarOpen(mobile, "Terminal"))
	if W == 1280 {
		step(`The tab ".terminal-panel-tab[title=\"printf toolbar-background\"]" should still be visible. Click its button named exactly "Close terminal"; the tab should be gone (count 0).`)
	}
	step(`Open the diffs: %s. ".diff-viewer-overlay" should be visible, and the first "select.diff-viewer-select" in it should have the value exactly "toolbar-fixture.txt". Press Escape; the overlay should become hidden.`, chatToolbarOpen(mobile, "Diffs"))
	step(`Open the git graph: %s. ".git-graph-container" should be visible, and one of its ".git-graph-subject" elements should contain "Toolbar fixture commit". Press Escape; it should become hidden.`, chatToolbarOpen(mobile, "Git Graph"))
	step(`Open the file finder: %s. The textbox named "Filter files" should be visible; fill it with "toolbar-fixture"; there should be exactly one ".grp-row". Press Enter in the textbox. The dialog named exactly "Edit {{FILE}}" (role dialog) should be visible, with a visible ".view-line" in it containing "after toolbar edit". Click the dialog's button named exactly "Close (Esc)"; the dialog should become hidden.`, chatToolbarOpen(mobile, "Edit File…"))
	if mobile {
		step(`Click the header's button named exactly "New Conversation". The URL path should become exactly "/new", the message input should be visible, and the VM link should be hidden.`)
	} else {
		step(`Click the header's button named exactly "New Conversation". The URL path should become exactly "/new", the message input should be visible, the toolbar's buttons named exactly "Diffs", "Git Graph" and "Terminal" should be visible, and the VM link should be visible.`)
	}
	e2e(t, useful, d.String(), func(s *shelley) {
		s.viewport(W, H)
		s.initScript(chatToolbarJS)
		chatToolbarLinks(s, true)
		dir := s.gitRepo(filepath.Join(s.dir, "toolbar"))
		file := s.write(filepath.Join(dir, "toolbar-fixture.txt"), "before toolbar edit\n")
		s.gitIn(dir, "add", ".")
		s.gitIn(dir, "commit", "-q", "-m", "Toolbar fixture commit")
		s.write(file, "after toolbar edit\n")
		s.v("FILE", file)
		slug := fmt.Sprintf("%d-very-long-conversation-title-that-must-not-crowd-the-toolbar", W)
		s.conversationWith(map[string]any{"message": "Hello", "model": "predictable", "cwd": dir}, slug)
		s.v("SLUG", slug)
	})
}

func TestE2EChatToolbarMissingVMLink320(t *testing.T)  { chatToolbarMissingVM(t, useful(0), 0) }
func TestE2EChatToolbarMissingVMLink393(t *testing.T)  { chatToolbarMissingVM(t, useful(0), 1) }
func TestE2EChatToolbarMissingVMLink768(t *testing.T)  { chatToolbarMissingVM(t, useful(0), 2) }
func TestE2EChatToolbarMissingVMLink1280(t *testing.T) { chatToolbarMissingVM(t, useful(0), 3) }

func chatToolbarMissingVM(t *testing.T, useful lazycue.Useful, i int) {
	W, H := chatToolbarViewports[i].width, chatToolbarViewports[i].height
	visible := 3
	if W < 768 {
		visible = 0
	}
	e2e(t, useful, chatToolbarSetup+fmt.Sprintf(`Without a VM link, every configured link stays in the overflow menu, at %dpx. The links are "Manage on exe.dev" (it opens https://exe.example/vm/vm) and "Documentation" (it opens https://docs.example/documentation). Perform the steps in order. 1. Navigate to /new and wait for the header. 2. Eval window.toolbarCounts() with expect "{\"buttons\":3,\"links\":0}". The toolbar should have exactly %d visible buttons (role button, count %d). 3. The header's [data-testid="live-call-header"] should be visible. 4. For each link name in turn, "Manage on exe.dev" then "Documentation": click More options; the menu's buttons named exactly "Manage on exe.dev" and "Documentation" should both be visible; click the one named exactly that name; it opens its page; the menu should then be hidden.`, W, visible, visible), func(s *shelley) {
		s.viewport(W, H)
		s.initScript(chatToolbarJS)
		chatToolbarLinks(s, false)
	})
}

func TestE2EChatToolbarGitGraphDirectOnDesktop(t *testing.T) {
	e2e(t, useful(0), `On a wide screen, Git Graph remains a direct toolbar action. 1. Navigate to /new. 2. The button named exactly "Git Graph" directly in the toolbar, ".chat-workspace-actions button[aria-label='Git Graph']", should be visible. 3. Click it. 4. ".git-graph-container" should become visible.`, func(s *shelley) {
		s.viewport(1280, 800)
	})
}

// On exe.dev the server links the VM's management page; the Playwright test
// was skipped elsewhere, so this one has the page say it runs on exe.dev, as
// the server would there.
func TestE2EChatToolbarManageOnExeDev(t *testing.T) {
	e2e(t, useful(0), `On exe.dev, "Manage on exe.dev" opens this VM's management page. 1. Navigate to /new. 2. Click the button named exactly "More options". 3. The button named exactly "Manage on exe.dev" in ".chat-overflow-popover" should be visible, and there must be no button named exactly "Back to exe.dev" in it (count 0). 4. Click that button. 5. A popup opens (tab "popup"): its URL should come to be exactly "https://exe.dev/vm/myvm", and it should show the heading (role heading) named "VM management". 6. ".chat-overflow-popover" should be hidden (or absent). 7. Close tab "popup".`, func(s *shelley) {
		s.patchInit(map[string]any{
			"is_exe_dev": true,
			"hostname":   "myvm.exe.xyz",
			"links": []serverLink{
				{"myvm.exe.xyz", "https://myvm.exe.xyz"},
				{"Manage on exe.dev", "https://exe.dev/vm/myvm"},
			},
		})
		s.host("https://exe.dev", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/vm/myvm" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<h1>VM management</h1>")
		}))
	})
}
