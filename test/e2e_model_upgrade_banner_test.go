package test

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

// Ported from ui/e2e/model-upgrade-banner.spec.ts.

// modelUpgradeNext is the fake newer model the predictable model upgrades to.
var modelUpgradeNext = map[string]any{
	"id":                 "predictable-next",
	"display_name":       "Predictable Next",
	"ready":              true,
	"supports_images":    true,
	"supports_reasoning": false,
}

var modelUpgradeLink = map[string]any{"to": "predictable-next", "kind": "version", "line": "Predictable"}

// modelUpgradeOffer gives the predictable model, the only one the e2e server
// has, an upgrade link to a fake newer one, in both places the page reads its
// model list from: GET /api/models and the init data. Once dismissed is set,
// /api/models no longer links them.
func (s *shelley) modelUpgradeOffer(dismissed *atomic.Bool) {
	s.initScript(`const modelUpgradeNext = ` + mustJSON(modelUpgradeNext) + `;
const modelUpgradeLink = ` + mustJSON(modelUpgradeLink) + `;
const modelUpgradePrior = Object.getOwnPropertyDescriptor(window, '__SHELLEY_INIT__');
let modelUpgradeInit;
Object.defineProperty(window, '__SHELLEY_INIT__', {
  configurable: true,
  get: () => modelUpgradeInit,
  set: (value) => {
    modelUpgradePrior?.set?.(value);
    const init = modelUpgradePrior?.get?.() ?? value;
    modelUpgradeInit = { ...init, models: [modelUpgradeNext, ...(init.models || []).map((m) => m.id === 'predictable' ? { ...m, upgrade: modelUpgradeLink } : m)] };
  },
});`)
	s.route("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		var models []map[string]any
		if err := s.fetchJSON(r, &models); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out := []map[string]any{modelUpgradeNext}
		for _, m := range models {
			if m["id"] == "predictable" && (dismissed == nil || !dismissed.Load()) {
				m = maps.Clone(m)
				m["upgrade"] = modelUpgradeLink
			}
			out = append(out, m)
		}
		writeJSON(w, out)
	})
}

// modelUpgradeLand answers a conversation settings POST as if the switch to
// the fake model landed: the current settings with the posted ones over them.
func (s *shelley) modelUpgradeLand() {
	s.route("POST /api/conversation/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		posted := object(readBody(r))
		get, err := http.NewRequestWithContext(r.Context(), "GET", r.URL.String(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		get.RequestURI = r.RequestURI
		var current map[string]any
		if err := s.fetchJSON(get, &current); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		maps.Copy(current, posted)
		writeJSON(w, current)
	})
}

// modelUpgradeBodies records the JSON bodies of requests; call name returns
// the first one that keep accepts, compacted with sorted keys, or "none".
type modelUpgradeBodies struct {
	mu    sync.Mutex
	first string
}

func (s *shelley) modelUpgradeBodies(call string) *modelUpgradeBodies {
	b := &modelUpgradeBodies{}
	s.call(call, func() string {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.first == "" {
			return "none"
		}
		return b.first
	})
	return b
}

// add records body if it is the first.
func (b *modelUpgradeBodies) add(body []byte) {
	var v any
	if json.Unmarshal(body, &v) != nil {
		v = string(body)
	}
	if body == nil {
		v = "(no body)"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.first == "" {
		b.first = mustJSON(v)
	}
}

func TestE2EModelUpgradeBannerNewChat(t *testing.T) {
	e2e(t, useful(0), `Model upgrade banner: new chats show the upgrade before sending, and Switch updates the composer. The server offers an upgrade from the predictable model (display name "predictable") to a newer "Predictable Next" (ID predictable-next), and an init script sets localStorage "shelley_selected_model" to "predictable" before the page's own scripts on every load (already in place: steps must not set it). "The banner" is the element with data-testid "model-upgrade-banner". Perform the steps in order. 1. Navigate to /new. 2. The banner's text should contain "Newer version of Predictable available". 3. The banner's text should contain "predictable → Predictable Next". 4. Click the element with data-testid "model-upgrade-action". 5. There must then be exactly 0 banners. 6. The text of ".status-field-model .model-picker" should contain "Predictable Next" (it need not be visible on this phone-sized screen; check its text). 7. eval localStorage.getItem("shelley_selected_model") must give exactly "predictable-next". 8. There must be exactly 0 elements with data-testid "model-upgrade-all-banner".`, func(s *shelley) {
		s.modelUpgradeOffer(nil)
		s.initScript(`localStorage.setItem("shelley_selected_model", "predictable");`)
	})
}

func TestE2EModelUpgradeBannerDraft(t *testing.T) {
	e2e(t, useful(0), `Model upgrade banner: unsent drafts show the upgrade, and Switch saves the model without sending. The server offers an upgrade from the predictable model to a newer "Predictable Next" (ID predictable-next). The draft conversation {{ID}} (no slug yet) holds the unsent text "Not sent yet" on the predictable model; updating it (PUT /api/conversation/{{ID}}/draft) is faked, and call "switched draft" returns the JSON body of the first such PUT whose model is predictable-next, or "none". Perform the steps in order. 1. Navigate to /c/{{ID}}. 2. The text of the element with data-testid "model-upgrade-banner" should contain "Newer version of Predictable available". 3. Call "switched draft": it must return exactly "none". 4. Click the element with data-testid "model-upgrade-action". 5. Call "switched draft" until it returns exactly {"model":"predictable-next"} (that whole body and nothing else). 6. There must then be exactly 0 elements with data-testid "model-upgrade-banner". 7. The message input ([data-testid="message-input"]) must have the value exactly "Not sent yet".`, func(s *shelley) {
		s.modelUpgradeOffer(nil)
		var draft map[string]any
		s.api("POST", "/api/conversations/draft", map[string]any{"model": "predictable", "draft": "Not sent yet"}, &draft)
		id, _ := draft["conversation_id"].(string)
		if id == "" {
			s.fail("draft without an ID: %v", draft)
		}
		// Drafts have no slug yet; /c/null would fall back to the newest chat.
		s.v("ID", id)
		switched := s.modelUpgradeBodies("switched draft")
		s.route("/api/conversation/"+id+"/draft", func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			fields := object(body)
			if r.Method == "PUT" && fields["model"] == "predictable-next" {
				switched.add(body)
			}
			out := maps.Clone(draft)
			maps.Copy(out, fields)
			writeJSON(w, out)
		})
	})
}

func TestE2EModelUpgradeBannerSwitchSendsNewerModel(t *testing.T) {
	// The e2e server only has the predictable model, so the switch itself
	// cannot land here; this pins the target the click sends. Dismissal
	// covers the banner coming back down.
	e2e(t, useful(0), `Model upgrade banner: Switch sends the newer model. The server offers an upgrade from the predictable model (display name "predictable") to a newer "Predictable Next" (ID predictable-next). Call "settings model" returns the "model" field of the first conversation settings POST (POST /api/conversation/<id>/settings) the page sent, or "none". "The banner" is the element with data-testid "model-upgrade-banner". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The banner's text should contain "Newer version of Predictable available". 3. The banner's text should contain "predictable → Predictable Next". 4. Call "settings model": it must return exactly "none". 5. Click the element with data-testid "model-upgrade-action". 6. Call "settings model" until it returns exactly "predictable-next".`, func(s *shelley) {
		s.modelUpgradeOffer(nil)
		s.v("SLUG", s.conversation("echo: upgrade me", "").Slug)
		var mu sync.Mutex
		model := ""
		s.route("POST /api/conversation/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Model *string `json:"model"`
			}
			json.Unmarshal(readBody(r), &body)
			mu.Lock()
			if model == "" {
				model = "(no model field)"
				if body.Model != nil {
					model = *body.Model
				}
			}
			mu.Unlock()
			s.pass.ServeHTTP(w, r)
		})
		s.call("settings model", func() string {
			mu.Lock()
			defer mu.Unlock()
			if model == "" {
				return "none"
			}
			return model
		})
	})
}

func TestE2EModelUpgradeBannerOffersApplyAll(t *testing.T) {
	e2e(t, useful(0), `Model upgrade banner offers once to repeat the switch on other chats. The server offers an upgrade from the predictable model to a newer "Predictable Next" (ID predictable-next); there is another conversation still on the predictable model; switching a conversation's model lands (the settings POST is faked to succeed); and repeating the switch on the other chats (POST /api/model-upgrade/apply-all) is faked to answer that 2 chats were switched and 1 skipped. Call "apply all" returns the JSON body of the first apply-all request, or "none". "The offer" is the element with data-testid "model-upgrade-all-banner". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The element with data-testid "model-upgrade-banner" should be visible (the page has loaded the upgrade). 3. There must be exactly 0 offers. 4. Click the element with data-testid "model-upgrade-action". 5. The offer's text should contain "Switch your other". 6. The offer's text should contain "Predictable Next". 7. Call "apply all": it must return exactly "none". 8. Click the element with data-testid "model-upgrade-all-action". 9. The exact switch just made travels with the request: call "apply all" until it returns exactly {"from":"predictable","to":"predictable-next"}. 10. There must then be exactly 0 offers. 11. The count is said, not left to guesswork: the text of the element with data-testid "model-upgrade-all-result-banner" should contain "Switched 2 chats from". 12. Its text should contain "Skipped 1 chat still running".`, func(s *shelley) {
		s.modelUpgradeOffer(nil)
		s.conversation("echo: still on the old model", "")
		s.v("SLUG", s.conversation("echo: upgrade them all", "").Slug)
		s.modelUpgradeLand()
		applied := s.modelUpgradeBodies("apply all")
		s.route("/api/model-upgrade/apply-all", func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			applied.add(body)
			writeJSON(w, map[string]int{"upgraded": 2, "skipped": 1})
		})
	})
}

func TestE2EModelUpgradeBannerDismissOffer(t *testing.T) {
	e2e(t, useful(0), `Model upgrade banner: dismissing the offer to switch the other chats records the answer. The server offers an upgrade from the predictable model to a newer "Predictable Next"; there is another conversation still on the predictable model; switching a conversation's model lands (the settings POST is faked to succeed); and recording the answer (POST /api/model-upgrade/prompt-dismissed) is faked. Call "prompt dismissed" returns "sent" once the page has sent that request, else "none". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Click the element with data-testid "model-upgrade-action". 3. Call "prompt dismissed": it must return exactly "none". 4. Click the element with data-testid "model-upgrade-all-dismiss". 5. Call "prompt dismissed" until it returns exactly "sent". 6. There must be exactly 0 elements with data-testid "model-upgrade-all-banner".`, func(s *shelley) {
		s.modelUpgradeOffer(nil)
		s.conversation("echo: still on the old model", "")
		s.v("SLUG", s.conversation("echo: no thanks", "").Slug)
		s.modelUpgradeLand()
		var sent atomic.Bool
		s.route("/api/model-upgrade/prompt-dismissed", func(w http.ResponseWriter, r *http.Request) {
			sent.Store(true)
			w.WriteHeader(http.StatusNoContent)
		})
		s.call("prompt dismissed", func() string {
			if sent.Load() {
				return "sent"
			}
			return "none"
		})
	})
}

func TestE2EModelUpgradeBannerDismiss(t *testing.T) {
	e2e(t, useful(0), `Model upgrade banner: dismissing records the pair and hides the banner. The server offers an upgrade from the predictable model to a newer "Predictable Next" (ID predictable-next); dismissing it (POST /api/model-upgrade/dismiss) is faked, and afterwards the server's model list no longer offers it. Call "dismissed" returns the JSON body of the first dismiss request, or "none". Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. The element with data-testid "model-upgrade-banner" should be visible. 3. Call "dismissed": it must return exactly "none". 4. Click the element with data-testid "model-upgrade-dismiss". 5. Call "dismissed" until it returns exactly {"from":"predictable","to":"predictable-next"}. 6. There must then be exactly 0 elements with data-testid "model-upgrade-banner".`, func(s *shelley) {
		var dismissed atomic.Bool
		s.modelUpgradeOffer(&dismissed)
		s.v("SLUG", s.conversation("echo: dismiss me", "").Slug)
		bodies := s.modelUpgradeBodies("dismissed")
		s.route("/api/model-upgrade/dismiss", func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			bodies.add(body)
			dismissed.Store(true)
			w.WriteHeader(http.StatusNoContent)
		})
	})
}
