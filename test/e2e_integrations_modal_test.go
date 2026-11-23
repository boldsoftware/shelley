package test

import (
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/integrations-modal.spec.ts.

// integrationsEntry is an integration the fake server lists.
type integrationsEntry = map[string]any

var (
	integrationsGH = integrationsEntry{
		"name": "gh", "type": "github", "url": "https://github.int.example.test",
		"help": "git clone https://github.int.example.test/owner/project.git",
		"details": map[string]any{"repositories": []any{map[string]any{
			"name": "owner/project", "url": "https://github.com/owner/project",
			"clone_command": "git clone https://github.int.example.test/owner/project.git",
		}}},
	}
	integrationsLLM = integrationsEntry{
		"name": "llm", "type": "llm", "url": "https://llm.int.example.test",
		"details": map[string]any{"model_counts": []any{
			map[string]any{"provider": "anthropic", "mode": "managed"},
			map[string]any{"provider": "openai", "mode": "api_key", "chat": 3},
		}},
	}
	integrationsReflection = integrationsEntry{
		"name": "reflection", "type": "reflection", "url": "https://reflection.int.example.test",
		"help": "curl https://reflection.int.example.test/",
	}
	integrationsNotify = integrationsEntry{
		"name": "notify", "type": "notify", "url": "https://notify.int.example.test",
	}
	integrationsSlackPersonal = integrationsEntry{
		"name": "slack-hook", "type": "slack", "url": "https://slack-hook.int.example.test",
	}
	integrationsSlackTeam = integrationsEntry{
		"name": "slack-hook", "type": "slack", "team": true, "url": "https://slack-hook.team.example.test",
	}
	integrationsDefault = []integrationsEntry{integrationsGH, integrationsLLM, integrationsNotify, integrationsReflection}
)

// integrationsFake is the fake integrations API: the list (without
// details), each entry's details by name and scope, and 503 for any send a
// test does not fake itself.
type integrationsFake struct {
	mu      sync.Mutex
	entries []integrationsEntry
	details []string // the names whose details were asked for, in order
	copied  *string  // what the page last copied
	// hold, when set, may answer a GET of /api/integrations itself,
	// reporting whether it did.
	hold func(w http.ResponseWriter, r *http.Request) bool
}

// integrationsSetup makes the page an exe.dev VM's, with a clipboard that
// records what it is given, and fakes the integrations API with entries.
func integrationsSetup(s *shelley, entries []integrationsEntry) *integrationsFake {
	f := &integrationsFake{entries: entries}
	s.initScript(`(() => {
  let init;
  Object.defineProperty(window, "__SHELLEY_INIT__", {
    configurable: true,
    get: () => init,
    set: (value) => {
      init = { ...value, is_exe_dev: true };
    },
  });
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: {
      writeText: async (text) => {
        window.copiedIntegrationText = text;
        fetch("/__test/clipboard", { method: "POST", body: text });
      },
    },
  });
})();`)
	s.route("POST /__test/clipboard", func(w http.ResponseWriter, r *http.Request) {
		text := string(readBody(r))
		f.mu.Lock()
		f.copied = &text
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	s.route("/api/integrations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			http.Error(w, "Integration sends must be mocked", http.StatusServiceUnavailable)
			return
		}
		if f.hold != nil && f.hold(w, r) {
			return
		}
		f.serve(w, r)
	})
	s.route("/api/integrations/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Integration sends must be mocked", http.StatusServiceUnavailable)
	})
	return f
}

// serve answers a GET of the list, or of one entry's details.
func (f *integrationsFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	list := []integrationsEntry{}
	if name := q.Get("details"); name != "" {
		f.details = append(f.details, name)
		team := q.Get("team") == "true"
		for _, e := range f.entries {
			if isTeam := e["team"] == true; e["name"] == name && isTeam == team {
				list = append(list, e)
			}
		}
	} else {
		for _, e := range f.entries {
			brief := integrationsEntry{}
			for k, v := range e {
				if k != "details" {
					brief[k] = v
				}
			}
			list = append(list, brief)
		}
	}
	writeJSON(w, map[string]any{"integrations": list})
}

// set replaces the entries the fake lists.
func (f *integrationsFake) set(entries ...integrationsEntry) {
	f.mu.Lock()
	f.entries = entries
	f.mu.Unlock()
}

// integrationsSend fakes POSTs to path: answer gets the 1-based number and
// body of each.
func integrationsSend(s *shelley, path string, answer func(n int64, body map[string]any, w http.ResponseWriter, r *http.Request)) {
	var n atomic.Int64
	s.route("POST "+path, func(w http.ResponseWriter, r *http.Request) {
		answer(n.Add(1), object(readBody(r)), w, r)
	})
}

// integrationsHow says how the integrations tests open the panel and pick
// an integration.
const integrationsHow = `Name elements by ARIA role and accessible name, as Playwright's getByRole does; "named X" means an exact name match. "The modal" is ".integrations-modal". To "open the panel": press Control+k; ".command-palette-input" should be visible; fill it with "integration", click the first ".command-palette-item", and the modal should be visible. To "select" an integration N: click the first ".integrations-tabs button" (the list), then the ".integrations-name-btn" whose text starts with N followed by a space; ".integrations-detail-head strong" should then have exactly the text N. "The refresh button" is the modal's button named "Refresh"; "the Slack test button" is the button named "Test integration (webhook only)"; "the notify button" is the button named "Send test notification". To "close the modal": click the modal's button whose name contains "Close modal". To "start": navigate to /new, wait for the element with data-testid "message-input" to be visible, and open the panel. `

func TestE2EIntegrationsModalSlackSelectorWithSingleHook(t *testing.T) {
	e2e(t, useful(0), `The Slack hook selector stays visible with a single hook. The VM has one integration, a personal Slack hook "slack-hook". `+integrationsHow+`Perform the steps in order. 1. Start, and select slack-hook. 2. "#slack-hook" should be visible, and the texts of its options should be exactly ["slack-hook · Personal"]. 3. The Slack test button should be visible.`, func(s *shelley) {
		integrationsSetup(s, []integrationsEntry{integrationsSlackPersonal})
	})
}

func TestE2EIntegrationsModalWebhookOnlyTestExplainsBotFailures(t *testing.T) {
	e2e(t, useful(0), `The Slack panel offers an explicit webhook-only test and explains bot failures. The VM has two integrations named "slack-hook": a personal webhook and a team Slack bot whose help is {{HELP}}. A Slack test answers 422 "This is a Slack bot. This test only supports incoming webhooks." Call "sends" returns how many Slack tests the page has sent; call "last send" returns the JSON body of the last one ("none" before one). `+integrationsHow+`Perform the steps in order. 1. Start, and click the first ".integrations-name-btn". 2. The Slack test button should be enabled, and ".slack-access" should have exactly the text "For incoming webhooks only. Slack bots are not supported by this test." 3. Select the option with value "team:slack-hook" of "#slack-hook" (fill it with that value). The Slack test button should be enabled. 4. Click ".integrations-guide summary": ".integrations-guide pre" should have exactly the text {{HELP}}. 5. Call "sends": it must return "0". 6. Fill "#slack-message" with "Hello bot" and click the Slack test button. The element with role "alert" inside ".slack-integration" should have exactly the text "This is a Slack bot. This test only supports incoming webhooks." 7. Call "sends": it must return "1"; call "last send": it must return {"message":"Hello bot","name":"slack-hook","team":true}.`, func(s *shelley) {
		help := `curl --json '{"channel":"C123","text":"hello"}' https://slack-hook.team.example.test/api/chat.postMessage`
		bot := integrationsEntry{"help": help}
		for k, v := range integrationsSlackTeam {
			bot[k] = v
		}
		s.v("HELP", help)
		integrationsSetup(s, []integrationsEntry{integrationsSlackPersonal, bot})
		sends := s.counter("sends")
		var last atomic.Value
		last.Store("none")
		s.call("last send", func() string { return last.Load().(string) })
		integrationsSend(s, "/api/integrations/slack/test", func(n int64, body map[string]any, w http.ResponseWriter, r *http.Request) {
			last.Store(mustJSON(body))
			sends.Store(n)
			http.Error(w, "This is a Slack bot. This test only supports incoming webhooks.", http.StatusUnprocessableEntity)
		})
	})
}

func TestE2EIntegrationsModalRefreshSlackDestinations(t *testing.T) {
	e2e(t, useful(0), `Refresh updates the Slack destinations, and goes back to the list when the selected one is detached. The VM starts with one integration, a personal Slack hook "slack-hook". Call "attach team hook" attaches a team Slack hook also named "slack-hook"; call "detach team hook" detaches it again. `+integrationsHow+`Perform the steps in order. 1. Start, and select slack-hook. 2. Fill "#slack-message" with "Keep this draft". 3. Call "attach team hook", then click the refresh button. The texts of "#slack-hook option" should be exactly ["slack-hook · Personal", "slack-hook · Team"], and "#slack-message" should have the value exactly "Keep this draft". 4. Select the option with value "team:slack-hook" of "#slack-hook" (fill it with that value). 5. Call "detach team hook", then click the refresh button. There should be exactly 1 ".integrations-name-btn" and no ".integrations-detail" (count 0). 6. Click the ".integrations-name-btn": the texts of "#slack-hook option" should be exactly ["slack-hook · Personal"].`, func(s *shelley) {
		f := integrationsSetup(s, []integrationsEntry{integrationsSlackPersonal})
		s.call("attach team hook", func() string {
			f.set(integrationsSlackPersonal, integrationsSlackTeam)
			return "attached"
		})
		s.call("detach team hook", func() string {
			f.set(integrationsSlackPersonal)
			return "detached"
		})
	})
}

func TestE2EIntegrationsModalSlackScopesAndLockedSend(t *testing.T) {
	e2e(t, useful(0), `Same-name Slack hooks switch by scope, and a send locks the destination it was submitted to. The VM has two integrations named "slack-hook": a personal one and a team one at https://slack-hook.team.example.test. The first Slack test waits until call "release", then succeeds; later ones answer 502 "Slack rejected the test". Call "first send" returns the JSON body of the first Slack test ("none" before one). `+integrationsHow+`Perform the steps in order. 1. Start, and click the first ".integrations-name-btn". 2. "#slack-hook" should have the value exactly "personal:slack-hook". Fill "#slack-message" with "Keep this draft". 3. Select the option with value "team:slack-hook" of "#slack-hook" (fill it with that value): "#slack-hook" should have the value exactly "team:slack-hook", "#slack-message" still exactly "Keep this draft", and ".integrations-detail-head" should contain the text "slack · Team". 4. Click ".integrations-advanced summary": the first ".integrations-copy-line code" should have exactly the text "https://slack-hook.team.example.test". 5. Click the Slack test button. "#slack-hook" and "#slack-message" should be disabled, and so should the refresh button and the first ".integrations-tabs button". 6. Call "first send" until it returns {"message":"Keep this draft","name":"slack-hook","team":true}. 7. Call "release". The element with role "status" inside ".slack-integration" should have exactly the text "Message sent to slack-hook.", and "#slack-hook" should be enabled. 8. Click the Slack test button again: the element with role "alert" inside ".slack-integration" should contain the text "Slack rejected the test".`, func(s *shelley) {
		integrationsSetup(s, []integrationsEntry{integrationsSlackPersonal, integrationsSlackTeam})
		release := s.gate("release")
		var first atomic.Value
		first.Store("none")
		integrationsSend(s, "/api/integrations/slack/test", func(n int64, body map[string]any, w http.ResponseWriter, r *http.Request) {
			if n > 1 {
				http.Error(w, "Slack rejected the test", http.StatusBadGateway)
				return
			}
			first.Store(mustJSON(body))
			if release.wait(r) {
				w.WriteHeader(http.StatusNoContent)
			}
		})
		s.call("first send", func() string { return first.Load().(string) })
	})
}

func TestE2EIntegrationsModalGitHubRepositoryCopiesClone(t *testing.T) {
	e2e(t, useful(0), `The GitHub integration shows its repository from the details and copies its clone command. The VM has four integrations: gh (GitHub, whose help is "git clone https://github.int.example.test/owner/project.git", with the repository owner/project), llm, notify and reflection. The page's clipboard is a fake: what is copied lands in window.copiedIntegrationText. Call "details asked" returns the names whose details the page has asked for, in order, as JSON. `+integrationsHow+`Perform the steps in order. 1. Start. There should be exactly 4 ".integrations-name-btn". 2. Call "details asked": it must return []. 3. Select gh. 4. "The table" is ".integrations-gh-table". Its link (role "link") whose name contains "owner/project" should have the attribute href exactly "https://github.com/owner/project". 5. Click the table's button named "Copy". window.copiedIntegrationText should become exactly "git clone https://github.int.example.test/owner/project.git" (eval until it is). 6. ".integrations-guide" should be there without the attribute "open": exactly 1 ".integrations-guide:not([open])". 7. Call "details asked": it must return ["gh"]. 8. The modal should hold no element with role "tab" (count 0). 9. Focus the first ".integrations-tabs button" (eval .focus()) and press Enter: there should be exactly 4 ".integrations-name-btn".`, func(s *shelley) {
		f := integrationsSetup(s, integrationsDefault)
		s.call("details asked", func() string {
			f.mu.Lock()
			defer f.mu.Unlock()
			return mustJSON(append([]string{}, f.details...))
		})
	})
}

// integrationsEditCommand is the test that the copied edit command keeps its
// SSH arguments intact, for a personal or team integration.
func integrationsEditCommand(t *testing.T, useful lazycue.Useful, team bool) {
	scope, flag, args := "personal", "", `"edit","gh",`
	if team {
		scope, flag, args = "team", " --team", `"edit","gh","--team",`
	}
	e2e(t, useful, `The copied edit command of a `+scope+` integration keeps its SSH arguments intact. The VM has one `+scope+` integration, gh (GitHub). The page's clipboard is a fake that tells the server what was copied. Call "ssh args" runs the copied command in sh with ssh defined as a function that prints its arguments, and returns them as a JSON array (each NUL-terminated, so the list ends with ""); call "edit args" does the same with the second of those arguments, run as a command with integrations defined that way ("none" before anything was copied). `+integrationsHow+`Perform the steps in order. 1. Start, and select gh. 2. Click ".integrations-advanced summary", then the button named "Copy edit command". The button inside the second ".integrations-copy-line" should have exactly the text "Copied". 3. Call "ssh args": it must return ["exe.dev","integrations edit 'gh'`+flag+` --comment='<new comment>'",""]. 4. Call "edit args": it must return [`+args+`"--comment=<new comment>",""].`, func(s *shelley) {
		gh := integrationsEntry{}
		for k, v := range integrationsGH {
			gh[k] = v
		}
		gh["team"] = team
		f := integrationsSetup(s, []integrationsEntry{gh})
		run := func(fn, command string) []string {
			out, err := exec.Command("sh", "-c", fn+"() { printf '%s\\0' \"$@\"; }\n"+command).Output()
			s.check(err)
			return strings.Split(string(out), "\x00")
		}
		copied := func() (string, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.copied == nil {
				return "", false
			}
			return *f.copied, true
		}
		s.call("ssh args", func() string {
			c, ok := copied()
			if !ok {
				return "none"
			}
			return mustJSON(run("ssh", c))
		})
		s.call("edit args", func() string {
			c, ok := copied()
			if !ok {
				return "none"
			}
			ssh := run("ssh", c)
			if len(ssh) < 2 {
				return "no second argument in " + mustJSON(ssh)
			}
			return mustJSON(run("integrations", ssh[1]))
		})
	})
}

func TestE2EIntegrationsModalEditCommandPersonal(t *testing.T) {
	integrationsEditCommand(t, useful(0), false)
}

func TestE2EIntegrationsModalEditCommandTeam(t *testing.T) {
	integrationsEditCommand(t, useful(0), true)
}

func TestE2EIntegrationsModalReflectionWithoutAccountAccess(t *testing.T) {
	e2e(t, useful(0), `The panel shows what reflection reports without account access. The VM has four integrations: gh, llm (whose details list anthropic, mode managed, and openai, mode api_key with 3 chat models), notify, and reflection (at https://reflection.int.example.test, whose help is "curl https://reflection.int.example.test/"). `+integrationsHow+`Perform the steps in order. 1. Start, and select llm. 2. There should be exactly 2 ".integrations-providers-table tbody tr". With all whitespace removed from their text (an eval), the first row's must contain "anthropicexe.devmanaged—" and the second's "openaiapi_key3chat". 3. Select reflection. ".integrations-guide" should contain the text "curl https://reflection.int.example.test/". 4. Click ".integrations-advanced summary": ".integrations-advanced" should contain the text "https://reflection.int.example.test". 5. Select notify. The modal should hold no element with role "alert" (count 0), and the notify button should be enabled.`, func(s *shelley) {
		integrationsSetup(s, integrationsDefault)
	})
}

func TestE2EIntegrationsModalIgnoresStaleList(t *testing.T) {
	e2e(t, useful(0), `The panel ignores a stale list answer after it was reopened. The page's first request for the integrations list gets the list holding only gh, but only once call "release" is made; later requests get the list holding only llm at once. Call "lists asked" returns how many times the page has asked for the list; call "old list answered" says whether the first answer has been sent ("true" or "false"). `+integrationsHow+`Perform the steps in order. 1. Start. 2. Call "lists asked" until it returns "1". 3. Close the modal, then open the panel again. The reopened panel asks again at once, without waiting for the first answer: "Loading integrations" should be gone within 5s (wait_text_gone with timeout "5s"), and the texts of ".integrations-name-btn" should be exactly ["llm →"]. 4. Call "release", then call "old list answered" until it returns "true". Let the late answer land: eval new Promise((resolve) => requestAnimationFrame(() => setTimeout(resolve, 300))). 5. The texts of ".integrations-name-btn" should still be exactly ["llm →"].`, func(s *shelley) {
		f := integrationsSetup(s, integrationsDefault)
		release := s.gate("release")
		lists := s.counter("lists asked")
		answered := s.signal("old list answered")
		f.hold = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Query().Has("details") {
				return false
			}
			if lists.Add(1) > 1 {
				writeJSON(w, map[string]any{"integrations": []any{integrationsLLM}})
				return true
			}
			if !release.wait(r) {
				return true
			}
			writeJSON(w, map[string]any{"integrations": []any{integrationsGH}})
			w.(http.Flusher).Flush()
			answered.Store(true)
			return true
		}
	})
}

func TestE2EIntegrationsModalAbandonedDetailLoading(t *testing.T) {
	e2e(t, useful(0), `Abandoned detail loading is cleared without applying its late result. The VM has four integrations: gh, llm (with provider details), notify and reflection. The page's first request for gh's details waits until call "release". Call "gh details asked" says whether the page has asked for gh's details ("true" or "false"); call "old details answered" whether the held answer has been sent. `+integrationsHow+`Perform the steps in order. 1. Start, and select gh. 2. Call "gh details asked" until it returns "true". 3. Close the modal, then open the panel again. There should be exactly 4 ".integrations-name-btn", and the refresh button should be enabled. 4. Call "release", then call "old details answered" until it returns "true". 5. Select llm. ".integrations-providers-table" should be visible, and there should be no ".integrations-guide" (count 0).`, func(s *shelley) {
		f := integrationsSetup(s, integrationsDefault)
		release := s.gate("release")
		asked := s.signal("gh details asked")
		answered := s.signal("old details answered")
		var held atomic.Bool
		f.hold = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Query().Get("details") != "gh" || held.Swap(true) {
				return false
			}
			asked.Store(true)
			if release.wait(r) {
				writeJSON(w, map[string]any{"integrations": []any{integrationsGH}})
				w.(http.Flusher).Flush()
				answered.Store(true)
			}
			return true
		}
	})
}

func TestE2EIntegrationsModalNotifySendLockedAfterReopen(t *testing.T) {
	e2e(t, useful(0), `A notification send stays locked after the panel is reopened, and its stale result is discarded. The VM has four integrations: gh, llm, notify and reflection. The first test notification waits until call "release", then succeeds; the second succeeds at once; the third answers 502 "Gateway rejected test". Call "sends" returns how many test notifications the page has sent; call "first message" returns the message of the first ("none" before one); call "first answered" says whether its answer has been sent ("true" or "false"). "The input" is "#integrations-test-message". `+integrationsHow+`Perform the steps in order. 1. Start, and select notify. 2. Fill the input with "My notification test" and click the notify button. The input should be disabled. 3. Call "first message" until it returns "My notification test". 4. Close the modal, open the panel again, and click the ".integrations-name-btn" whose text starts with "notify ". 5. The input should be disabled, and so should ".integrations-notify button". 6. Submit ".integrations-notify form" anyway with an eval dispatching new Event("submit", {bubbles: true, cancelable: true}) on it. Sleep 500ms; call "sends": it must return "1". 7. Call "release", then call "first answered" until it returns "true". Let the answer land: eval new Promise((resolve) => requestAnimationFrame(() => setTimeout(resolve, 300))). 8. The input should be enabled, and ".integrations-notify" should hold no element with role "status" (count 0). 9. Click the notify button: the element with role "status" inside ".integrations-notify" should contain the text "Test accepted". 10. Click the notify button again: the element with role "alert" inside ".integrations-notify" should contain the text "Gateway rejected test".`, func(s *shelley) {
		integrationsSetup(s, integrationsDefault)
		release := s.gate("release")
		sends := s.counter("sends")
		answered := s.signal("first answered")
		var first atomic.Value
		first.Store("none")
		s.call("first message", func() string { return first.Load().(string) })
		integrationsSend(s, "/api/integrations/notify/test", func(n int64, body map[string]any, w http.ResponseWriter, r *http.Request) {
			sends.Store(n)
			switch n {
			case 1:
				msg, _ := body["message"].(string)
				first.Store(msg)
				if !release.wait(r) {
					return
				}
				w.WriteHeader(http.StatusNoContent)
				w.(http.Flusher).Flush()
				answered.Store(true)
			case 2:
				w.WriteHeader(http.StatusNoContent)
			default:
				http.Error(w, "Gateway rejected test", http.StatusBadGateway)
			}
		})
	})
}

func TestE2EIntegrationsModalSlackSendLockedAfterReopen(t *testing.T) {
	e2e(t, useful(0), `A Slack send stays locked after the panel is reopened, and is released after a failure that happened while it was hidden. The VM has one integration, a personal Slack hook "slack-hook". The first Slack test waits until call "release", then answers 502 "Old send failed"; later ones succeed. Call "sends" returns how many Slack tests the page has sent; call "first answered" says whether the first's answer has been sent ("true" or "false"). "The input" is "#slack-message". `+integrationsHow+`Perform the steps in order. 1. Start, and select slack-hook. 2. Fill the input with "Keep this draft" and click the Slack test button. Call "sends" until it returns "1". 3. Press Escape: the modal should be hidden. 4. Open the panel and click the ".integrations-name-btn". The input should be disabled with the value exactly "Keep this draft", and "#slack-hook" should be disabled. 5. Submit ".slack-integration form" anyway with an eval dispatching new Event("submit", {bubbles: true, cancelable: true}) on it. Sleep 500ms; call "sends": it must return "1". 6. Press Escape (the modal should be hidden). Call "release", then call "first answered" until it returns "true". 7. Open the panel and click the ".integrations-name-btn". The input should be enabled, and ".slack-integration" should hold no element with role "alert" (count 0). 8. Click the Slack test button: the element with role "status" inside ".slack-integration" should have exactly the text "Message sent to slack-hook.".`, func(s *shelley) {
		integrationsSetup(s, []integrationsEntry{integrationsSlackPersonal})
		release := s.gate("release")
		sends := s.counter("sends")
		answered := s.signal("first answered")
		integrationsSend(s, "/api/integrations/slack/test", func(n int64, body map[string]any, w http.ResponseWriter, r *http.Request) {
			sends.Store(n)
			if n > 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if !release.wait(r) {
				return
			}
			http.Error(w, "Old send failed", http.StatusBadGateway)
			w.(http.Flusher).Flush()
			answered.Store(true)
		})
	})
}

func TestE2EIntegrationsModalSelectedLanguage(t *testing.T) {
	e2e(t, useful(0), `The panel and its commands use the selected language, Spanish. The VM has five integrations: gh, llm, notify, reflection and a personal Slack hook "slack-hook". A Slack test answers 422 "This is a Slack bot. This test only supports incoming webhooks." `+integrationsHow+`Perform the steps in order. 1. Start. 2. ".integrations-modal .modal-title" must not have exactly the text "VM integrations", and should contain the text "integraciones" in any case (an eval lowercasing its text). 3. Select notify. ".integrations-notify button" must not have exactly the text "Send test notification". 4. Select slack-hook. Click the button named "Probar integración (solo webhook)". The element with role "alert" inside ".slack-integration" should have exactly the text "Esta integración es un bot de Slack. Esta prueba solo admite webhooks entrantes."`, func(s *shelley) {
		integrationsSetup(s, append(slices.Clone(integrationsDefault), integrationsSlackPersonal))
		s.initScript(`localStorage.setItem("shelley-locale", "es");`)
		integrationsSend(s, "/api/integrations/slack/test", func(n int64, body map[string]any, w http.ResponseWriter, r *http.Request) {
			http.Error(w, "This is a Slack bot. This test only supports incoming webhooks.", http.StatusUnprocessableEntity)
		})
	})
}

func TestE2EIntegrationsModalLocksWhileNotifySends(t *testing.T) {
	e2e(t, useful(0), `Refresh and the tabs are locked while a notification is sending. The VM has four integrations: gh, llm, notify and reflection. A test notification waits until call "release", then succeeds. Call "sent" says whether the page has sent one ("true" or "false"). `+integrationsHow+`Perform the steps in order. 1. Start, and select notify. 2. Click the notify button, then call "sent" until it returns "true". 3. The refresh button should be disabled, and so should the first ".integrations-tabs button". 4. Call "release". The element with role "status" inside ".integrations-notify" should contain the text "Test accepted", and the refresh button should be enabled.`, func(s *shelley) {
		integrationsSetup(s, integrationsDefault)
		release := s.gate("release")
		sent := s.signal("sent")
		integrationsSend(s, "/api/integrations/notify/test", func(n int64, body map[string]any, w http.ResponseWriter, r *http.Request) {
			sent.Store(true)
			if release.wait(r) {
				w.WriteHeader(http.StatusNoContent)
			}
		})
	})
}
