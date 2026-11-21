package test

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

// Helpers the ported workspace, diff viewer and file finder specs share
// (ui/e2e/helpers.ts's withTempDir, initGitRepo and openWorkspaceTool).

// tempDir is a fresh directory of the attempt's own, outside the server's
// working directory, as withTempDir made one.
func (s *shelley) tempDir(name string) string {
	return s.mkdir(filepath.Join(s.dir, name))
}

// gitRepo makes dir a git repository the server's own git commands (an
// agent's commits, say) can commit in, as initGitRepo did.
func (s *shelley) gitRepo(dir string) string {
	s.mkdir(dir)
	s.gitIn(dir, "init", "--quiet")
	s.gitIn(dir, "config", "user.name", "Shelley Test")
	s.gitIn(dir, "config", "user.email", "test@example.com")
	s.gitIn(dir, "config", "commit.gpgsign", "false")
	s.gitIn(dir, "config", "core.hooksPath", "/dev/null")
	return dir
}

// conversationIn starts a conversation in dir, as conversation does in the
// working directory.
func (s *shelley) conversationIn(dir, message string) convo {
	return s.conversationWith(map[string]any{"message": message, "model": "predictable", "cwd": dir}, "")
}

// openToolWide and openToolPhone say how a step opens a workspace tool
// (openWorkspaceTool): wide screens have it in the header, phones in the
// overflow menu.
const (
	openToolWide  = `click the button with aria-label exactly "%[1]s" in the header's workspace toolbar (".chat-workspace-actions")`
	openToolPhone = `on a phone it is in the overflow menu: click the button named "More options", then, in its popover (".chat-overflow-popover"), the button whose accessible name starts with "%[1]s" (".overflow-toolbar-action[aria-label=\"%[1]s\"]")`
)

// shelleyAPI is api with the X-Shelley-Request header the UI's own requests
// carry, which some endpoints require.
func (s *shelley) shelleyAPI(method, path string, body, out any) {
	s.start()
	var r io.Reader
	if body != nil {
		r = strings.NewReader(mustJSON(body))
	}
	req, err := http.NewRequestWithContext(s.ctx, method, s.backend+path, r)
	s.check(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shelley-Request", "1")
	resp, err := http.DefaultClient.Do(req)
	s.check(err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	s.check(err)
	if resp.StatusCode/100 != 2 {
		s.fail("%s %s: %s: %s", method, path, resp.Status, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			s.fail("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}
