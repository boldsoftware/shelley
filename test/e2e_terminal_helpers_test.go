package test

import (
	"bufio"
	"net"
	"net/http"
	"path/filepath"
	"sync"
)

// Shared by the ports of ui/e2e/terminal-*.spec.ts.

// terminalConversation starts a conversation, renamed to slug, in a new
// directory dir of the working directory holding files, as the specs'
// mkdtemp directories did. It sets {{TERM_DIR}} to the directory.
func (s *shelley) terminalConversation(dir, message, slug string, files map[string]string) (convo, string) {
	path := s.mkdir(dir)
	for name, content := range files {
		s.write(filepath.Join(path, name), content)
	}
	s.v("TERM_DIR", path)
	return s.conversationWith(map[string]any{"message": message, "model": "predictable", "cwd": path}, slug), path
}

// terminalRenderer has terminals render with renderer ("ghostty" or
// "xterm"), as if chosen in the terminal panel's menu.
func (s *shelley) terminalRenderer(renderer string) {
	s.initScript(`localStorage.setItem("shelley-terminal-renderer", "` + renderer + `");`)
}

const (
	// visibleTerminal is the terminal the panel shows.
	visibleTerminal = `.terminal-panel-content [data-terminal-id][style*="display: block"]`

	// termMenu says how to choose an item of the terminal menu, as the
	// specs' chooseTerminalMenuItem did.
	termMenu = `To choose a terminal menu item, click the button with aria-label exactly "Terminal menu", then the button with exactly that text inside ".overflow-menu-panel" (two real clicks). `

	// termInput names the visible terminal's input.
	termInput = `"The terminal input" is the visible terminal's input, '` + visibleTerminal + ` textarea[aria-label="Terminal input"]' (other terminals' inputs are hidden); to type into it, focus it with an eval (.focus()), then use type steps, which type key by key, and press_key for Enter or Control+d. `
)

// hijacked passes a request on, telling conn about the connection the
// response hijacks: a websocket the proxy upgrades.
type hijacked struct {
	http.ResponseWriter
	conn func(net.Conn)
}

func (h *hijacked) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(h.ResponseWriter).Hijack()
	if err == nil {
		h.conn(c)
	}
	return c, rw, err
}

func (h *hijacked) Unwrap() http.ResponseWriter { return h.ResponseWriter }

// droppableSockets passes the browser's terminal websockets on, and returns
// a function that drops (closes without a close frame) the connections of
// those whose URL matches, as a lost network would, reporting whether there
// was one.
func (s *shelley) droppableSockets(match func(r *http.Request) bool) func() bool {
	var mu sync.Mutex
	var conns []net.Conn
	s.route("GET /api/exec-ws", func(w http.ResponseWriter, r *http.Request) {
		if !match(r) {
			s.pass.ServeHTTP(w, r)
			return
		}
		s.pass.ServeHTTP(&hijacked{ResponseWriter: w, conn: func(c net.Conn) {
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}}, r)
	})
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
		dropped := len(conns) > 0
		conns = nil
		return dropped
	}
}
