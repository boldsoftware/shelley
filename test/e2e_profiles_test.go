package test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/profiles.spec.ts.

// profilesDesktop is the spec's 1280x800 desktop window.
var profilesDesktop = lazycue.Device{Width: 1280, Height: 800, Scale: 1}

// profilesHow names what the profiles tests use.
const profilesHow = `The screen is a 1280x800 desktop window. Name elements by ARIA role and accessible name, as Playwright's getByRole does; "named X" means an exact name match, "whose name contains X" a substring one. "The panel" is ".model-picker-panel". To "open the knob": click ".model-picker-inline .p-select-label"; the panel should be visible. To "edit tools": click the button whose name contains "Edit" inside the panel's data-testid "model-picker-tools"; the panel should be hidden. "The pills" are data-testid "model-picker-profiles"; "the profile X" is the radio (role "radio") named X inside them. To "edit profiles": click the button whose name contains "Edit" inside the pills. "The section" is data-testid "model-picker-auto-compaction"; "the section's X" is the radio named X inside it, and "the nudge" is its select (role "combobox"). "The dialog" is the element with role "dialog". "The X tool" is the dialog's radiogroup with aria-label X ('[role=radiogroup][aria-label="X"]'). "The auto compaction row" is the dialog's data-testid "auto-compaction". To "apply": click the dialog's button whose name contains "Apply to this conversation"; the dialog should be hidden. "The markers" are the data-testid "message-modelchange" elements. "Changed" is the panel's data-testid "model-picker-profile-changed". To "save as new" a name N: click changed's button whose name contains "Save as new…", fill changed's textbox named "New profile name" with N and press Enter in it. A "row" X is the dialog's data-testid "profile-row" containing the text X. `

// profilesOptions is a conversation's options, as the server has them.
func profilesOptions(s *shelley, id string) map[string]any {
	var got struct {
		Conversation struct {
			Options string `json:"conversation_options"`
		} `json:"conversation"`
	}
	s.api("GET", "/api/conversation/"+id, nil, &got)
	return object([]byte(got.Conversation.Options))
}

// profilesSettings is a conversation's settings, as the server has them.
func profilesSettings(s *shelley, id string) map[string]any {
	var got map[string]any
	s.api("GET", "/api/conversation/"+id+"/settings", nil, &got)
	return got
}

// profilesOr is the JSON of v, or of empty when v is nil.
func profilesOr(v, empty any) string {
	if v == nil {
		return mustJSON(empty)
	}
	return mustJSON(v)
}

// profilesOverrides provides call name, the JSON of the conversation's
// tool overrides in its options ({} when unset).
func profilesOverrides(s *shelley, name, id string) {
	s.call(name, func() string { return profilesOr(profilesOptions(s, id)["tool_overrides"], map[string]any{}) })
}

// profilesStarted provides call "started settings": the given fields of the
// settings of the conversation the page's first send started ("none"
// before one).
func profilesStarted(s *shelley, keys ...string) {
	var mu sync.Mutex
	var id string
	found := func(v string) {
		mu.Lock()
		if id == "" {
			id = v
		}
		mu.Unlock()
	}
	s.route("POST /api/conversations/new", func(w http.ResponseWriter, r *http.Request) {
		resp, err := s.forward(r)
		if err != nil {
			abort(w, r)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if v, _ := object(b)["conversation_id"].(string); v != "" {
			found(v)
		}
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(b)
	})
	s.route("POST /api/conversation/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		found(r.PathValue("id"))
		s.pass.ServeHTTP(w, r)
	})
	s.call("started settings", func() string {
		mu.Lock()
		v := id
		mu.Unlock()
		if v == "" {
			return "none"
		}
		return modelProvidersSubset(profilesSettings(s, v), keys...)
	})
}

func TestE2EProfilesAssistantStartsTextChat(t *testing.T) {
	e2e(t, `The pre-installed Assistant profile starts a text chat with auto compaction. `+menusOpenModelPickerHow+profilesHow+`Perform the steps in order. 1. Navigate to /new and open the model picker. 2. The profile "Default" should have aria-checked="true". 3. Click the profile "Assistant", then edit tools. 4. The auto compaction row's radio named "On" (in its radiogroup named "Auto compaction") should have aria-checked="true", and the row's select (role "combobox") the value exactly "130000". The message_user tool's radio named "On" should have aria-checked="true". 5. Click the dialog's button whose name contains "Close modal". 6. Fill data-testid "message-input" with 'message_user: {"text":"Hey! What can I help with?","end_turn":true}' and click data-testid "send-button". 7. data-testid "message-user-text" should have exactly the text "Hey! What can I help with?". 8. ".messages-container" must not contain the text "edit predictable.go" (the reply did not use tools).`, func(s *shelley) {
		s.device(profilesDesktop)
	})
}

func TestE2EProfilesRestoreBuiltinAfterConfirmation(t *testing.T) {
	e2e(t, `A built-in profile is restored only after confirmation, without changing a live conversation's settings. The built-in profile "Default" has been customized (it now differs from its built-in settings, voice prompt included), a profile "E2E restore" exists, and the conversation /c/{{SLUG}} uses "Default". Call "Default" returns "edited" while the stored Default profile is the customized one, "built-in" once it equals the built-in one again (otherwise its JSON); call "default profile" returns "unchanged" while the default profile is the one it was at the start; call "conversation settings" returns "unchanged" while the conversation's settings are what they were at the start; call "restore answer" returns "ok" once the server answered the page's restore of Default with success ("none" before one, otherwise the status). `+profilesHow+`"The row" is the dialog's data-testid "profile-row" whose ".profiles-item-name" has exactly the text "Default"; "restore" is its button named "Restore defaults", and "confirm" its button named "Restore “Default”". To "open the profiles": open the knob, then edit profiles. Perform the steps in order. 1. Navigate to /c/{{SLUG}} and open the profiles. 2. The row "E2E restore" must have no button whose name contains "Restore defaults" (count 0). 3. Restore should be enabled; click it. The row should contain the text "voice prompt". Call "Default": it must return "edited". 4. Click the row's button named "Cancel": there should be no confirm (count 0). 5. Click restore again, then the dialog's button whose name contains "Close modal". Open the profiles again: there should be no confirm (count 0). 6. Click restore, then confirm. Call "restore answer" until it returns "ok". Restore should be disabled. 7. Call "Default": it must return "built-in"; call "default profile": "unchanged"; call "conversation settings": "unchanged". 8. Click the dialog's button whose name contains "Close modal" and open the knob: changed should contain the text "Differs from Default".`, func(s *shelley) {
		s.device(profilesDesktop)
		type profile = map[string]any
		var before []profile
		s.api("GET", "/api/profiles", nil, &before)
		var builtin profile
		defaultName := ""
		for _, p := range before {
			if p["name"] == "Default" {
				builtin = p
			}
			if p["default"] == true {
				defaultName, _ = p["name"].(string)
			}
		}
		if builtin == nil || builtin["builtin"] != true || builtin["user_edited"] != false {
			s.fail("built-in Default: %v", builtin)
		}
		s.api("POST", "/api/profiles", map[string]any{"name": "E2E restore"}, nil)
		var edited profile
		s.api("PUT", "/api/profiles/Default", map[string]any{
			"model":                "predictable",
			"tool_overrides":       map[string]any{"browser": "off", "compact_in_place": "off", "message_user": "off"},
			"compact_nudge_tokens": 42000,
			"system_prompt":        "Keep these conversation settings.",
			"voice_prompt":         "Speak as a pirate.",
		}, &edited)
		if edited["user_edited"] != true {
			s.fail("customized Default is not user_edited: %v", edited)
		}
		c := s.conversation("echo: restore profile", "")
		s.v("SLUG", c.Slug)
		s.api("POST", "/api/conversation/"+c.ID+"/settings", map[string]any{"profile": "Default"}, nil)
		settings := mustJSON(profilesSettings(s, c.ID))
		list := func() []profile {
			var got []profile
			s.api("GET", "/api/profiles", nil, &got)
			return got
		}
		s.call("Default", func() string {
			for _, p := range list() {
				if p["name"] != "Default" {
					continue
				}
				switch mustJSON(p) {
				case mustJSON(edited):
					return "edited"
				case mustJSON(builtin):
					return "built-in"
				}
				return mustJSON(p)
			}
			return "no Default"
		})
		s.call("default profile", func() string {
			for _, p := range list() {
				if p["default"] == true {
					if p["name"] == defaultName {
						return "unchanged"
					}
					return "now " + mustJSON(p["name"])
				}
			}
			return "no default"
		})
		s.call("conversation settings", func() string {
			if got := mustJSON(profilesSettings(s, c.ID)); got != settings {
				return got
			}
			return "unchanged"
		})
		var mu sync.Mutex
		answer := "none"
		s.route("POST /api/profiles/Default/restore", func(w http.ResponseWriter, r *http.Request) {
			resp, err := s.forward(r)
			if err != nil {
				abort(w, r)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			mu.Lock()
			if resp.StatusCode/100 == 2 {
				answer = "ok"
			} else {
				answer = resp.Status
			}
			mu.Unlock()
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)
			w.Write(b)
		})
		s.call("restore answer", func() string {
			mu.Lock()
			defer mu.Unlock()
			return answer
		})
	})
}

func TestE2EProfilesToolsEditChangesLiveConversation(t *testing.T) {
	e2e(t, `Tools' Edit… changes a live conversation's tools. Call "tool overrides" returns the tool overrides in the conversation's options as JSON ({} when unset). `+profilesHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}, open the knob and edit tools. 2. Click the browser tool's radio whose name contains "Off", then apply. 3. There should be exactly one marker, with the attribute aria-label exactly "Tools turned off: browser.". 4. Call "tool overrides": it must return {"browser":"off"}.`, func(s *shelley) {
		s.device(profilesDesktop)
		c := s.conversation("echo: tools later", "")
		s.v("SLUG", c.Slug)
		profilesOverrides(s, "tool overrides", c.ID)
	})
}

func TestE2EProfilesAutoCompactionFromPicker(t *testing.T) {
	e2e(t, `Auto compaction, on by default, is switched from the picker. Call "tool overrides" returns the tool overrides in the conversation's options as JSON ({} when unset); call "nudge tokens" returns their compact_nudge_tokens ("absent" when unset). "The tools text" is the panel's data-testid "model-picker-tools" ".model-picker-section-text". `+profilesHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}} and open the knob. 2. The section's "On" should have aria-checked="true", and the nudge the value exactly "200000". 3. The panel stays open through each change: select "250000" in the nudge (fill it with that value). There should be exactly 1 marker; the nudge should have the value "250000"; the tools text should be exactly "All at their defaults". Call "nudge tokens": it must return "250000". 4. Click the section's "Off". The last marker should have the attribute aria-label exactly "Tools turned off: compact_in_place."; the section's "Off" should have aria-checked="true"; the nudge should be hidden; the tools text still exactly "All at their defaults". Call "tool overrides": it must return {"compact_in_place":"off"}. 5. Click the section's "On". There should be exactly 3 markers, and the section's "On" should have aria-checked="true". Call "tool overrides": it must return {}.`, func(s *shelley) {
		s.device(profilesDesktop)
		c := s.conversation("echo: compaction later", "")
		s.v("SLUG", c.Slug)
		profilesOverrides(s, "tool overrides", c.ID)
		s.call("nudge tokens", func() string {
			v, ok := profilesOptions(s, c.ID)["compact_nudge_tokens"]
			if !ok {
				return "absent"
			}
			return mustJSON(v)
		})
	})
}

func TestE2EProfilesQuickSecondPickUndoesFirst(t *testing.T) {
	e2e(t, `A quick second pick of auto compaction undoes the first, even when the first's change is held on its way. Call "hold" makes the server hold the next settings change the page sends (a POST of the conversation's settings) until call "release", as a slow network would; call "posts" returns how many of the settings changes sent since the last "hold" the server has finished answering. Call "mark" notes the moment; call "next post" returns the JSON body of the first settings change sent after the mark ("none" before one). Call "tool overrides" returns the tool overrides in the conversation's options as JSON ({} when unset); call "nudge tokens" returns their compact_nudge_tokens (0 when unset). `+profilesHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}} and open the knob. 2. Call "hold". Select "250000" in the nudge, then "200000" (fill it with each value). Call "release", then call "posts" until it returns "2". Call "nudge tokens" until it returns "0". The nudge should have the value exactly "200000". 3. Call "hold". Click the section's "Off", then the section's "On". Call "release", then call "posts" until it returns "2". Call "tool overrides" until it returns {}. The section's "On" should have aria-checked="true". 4. The choice in effect changes nothing: call "mark", click the section's "On", then select "300000" in the nudge. Call "next post" until it returns {"compact_nudge_tokens":300000}.`, func(s *shelley) {
		s.device(profilesDesktop)
		c := s.conversation("echo: change my mind", "")
		s.v("SLUG", c.Slug)
		profilesOverrides(s, "tool overrides", c.ID)
		s.call("nudge tokens", func() string { return profilesOr(profilesOptions(s, c.ID)["compact_nudge_tokens"], 0) })
		var mu sync.Mutex
		var held chan struct{}
		// posts counts the changes that arrived, answered those the server
		// has finished with: only then has a change taken effect.
		posts, answered, mark := 0, 0, -1
		var bodies []string
		s.route("POST /api/conversation/"+c.ID+"/settings", func(w http.ResponseWriter, r *http.Request) {
			body := readBody(r)
			mu.Lock()
			bodies = append(bodies, mustJSON(object(body)))
			first := posts == 0
			posts++
			wait := held
			mu.Unlock()
			if first && wait != nil {
				select {
				case <-wait:
				case <-r.Context().Done():
					return
				}
			}
			s.pass.ServeHTTP(w, r)
			mu.Lock()
			answered++
			mu.Unlock()
		})
		s.call("hold", func() string {
			mu.Lock()
			defer mu.Unlock()
			held, posts, answered = make(chan struct{}), 0, 0
			return "holding"
		})
		s.call("release", func() string {
			mu.Lock()
			defer mu.Unlock()
			if held != nil {
				close(held)
				held = nil
			}
			return "released"
		})
		s.call("posts", func() string {
			mu.Lock()
			defer mu.Unlock()
			return strconv.Itoa(answered)
		})
		s.call("mark", func() string {
			mu.Lock()
			defer mu.Unlock()
			mark = len(bodies)
			return "marked"
		})
		s.call("next post", func() string {
			mu.Lock()
			defer mu.Unlock()
			if mark < 0 || len(bodies) <= mark {
				return "none"
			}
			return bodies[mark]
		})
	})
}

func TestE2EProfilesNoToolsAutoCompactionOffUntilOn(t *testing.T) {
	e2e(t, `In a conversation started with -no-tools, auto compaction is off until turned on. Call "tool overrides" returns the tool overrides in the conversation's options as JSON ({} when unset). `+profilesHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}} and open the knob. 2. The section's "Off" (in its radiogroup named "Auto compaction") should have aria-checked="true". 3. Edit tools. The auto compaction row's radio whose name contains "Off" should have aria-checked="true", and the browser tool's radio whose name contains "Default (off)" should have aria-checked="true". 4. Click the auto compaction row's radio whose name contains "On", then apply. 5. Call "tool overrides": it must return {"compact_in_place":"on"}.`, func(s *shelley) {
		s.device(profilesDesktop)
		c := s.conversationWith(map[string]any{
			"message": "echo: no tools", "model": "predictable", "cwd": s.cwd,
			"conversation_options": map[string]any{"disable_all_tools": true},
		}, "")
		s.v("SLUG", c.Slug)
		profilesOverrides(s, "tool overrides", c.ID)
	})
}

// profilesComposerNudge is the test that a composer nudge a browser kept
// from when 250k was the default shows as the given value.
func profilesComposerNudge(t *testing.T, kept, shown string) {
	e2e(t, `The composer's auto compaction nudge kept in this browser as `+kept+` shows as `+shown+`. The browser has kept, from earlier, the applied profile "Default" with the nudge `+kept+`. `+menusOpenModelPickerHow+profilesHow+`Perform the steps in order. 1. Navigate to /new and open the model picker. 2. The nudge should have the value exactly "`+shown+`". 3. Reload the page and open the model picker again: the nudge should still have the value exactly "`+shown+`".`, func(s *shelley) {
		s.device(profilesDesktop)
		s.initScript(`if (!localStorage.getItem("shelley.profileApplied")) {
  localStorage.setItem("shelley.profileApplied", "Default");
  localStorage.setItem("shelley.compactNudgeTokens", ` + mustJSON(kept) + `);
}`)
	})
}

func TestE2EProfilesComposerNudgeKept250000(t *testing.T) {
	profilesComposerNudge(t, "250000", "200000")
}

func TestE2EProfilesComposerNudgeKept130000(t *testing.T) {
	profilesComposerNudge(t, "130000", "130000")
}

func TestE2EProfilesOutputHtmlOffFollowsOutputIframeOnce(t *testing.T) {
	e2e(t, `New chats keep output_iframe's off for output_html, once. To "store O" is to eval localStorage.setItem("shelley.toolOverrides", JSON.stringify(O)) for the object O; to "store O fresh" is to do that and also eval localStorage.removeItem("shelley.toolOverrides.outputHtml"). "The tools text" is ".model-picker-section-text" inside the panel's data-testid "model-picker-tools". `+menusOpenModelPickerHow+profilesHow+`Perform the steps in order. 1. Navigate to /new. The composer first takes on its profile's settings: eval localStorage.getItem("shelley.profileApplied") until it returns "Default". 2. Store {"output_iframe":"off"} fresh. Reload the page and open the model picker: the tools text should contain "output_html". 3. Later, output_html back at its default stays there: store {"output_iframe":"off"}, not fresh. Reload the page and open the model picker: the tools text should have exactly the text "All at their defaults".`, func(s *shelley) {
		s.device(profilesDesktop)
	})
}

func TestE2EProfilesComposerCompactionOff(t *testing.T) {
	e2e(t, `Auto compaction turned off in the composer starts the conversation without it. Call "started settings" returns the profile and tool_overrides of the settings of the conversation the page's first send started, as JSON ("none" before one). `+menusOpenModelPickerHow+profilesHow+`Perform the steps in order. 1. Navigate to /new and open the model picker. 2. Assistant's idle compaction needs auto compaction: click the profile "Assistant". The section's "On" should have aria-checked="true", the section's "Off" should be disabled, and the section should contain the text "idle compaction needs it". 3. Click the profile "Default". The section's "Off" should be enabled; click it: it should have aria-checked="true". 4. Press Escape. Fill data-testid "message-input" with "echo: no compaction" and click data-testid "send-button". 5. Call "started settings" until it returns {"profile":"Default","tool_overrides":{"compact_in_place":"off"}}.`, func(s *shelley) {
		s.device(profilesDesktop)
		profilesStarted(s, "profile", "tool_overrides")
	})
}

func TestE2EProfilesSaveEditAndSwitch(t *testing.T) {
	e2e(t, `Changed settings are saved as a profile, which is edited and switched. Call "last system prompt" returns "ok" when the conversation's last system message's text is "Be brief in " followed by an absolute path and ".\n" (otherwise that text, JSON-encoded). `+profilesHow+`"The prompt" is the dialog's textbox named "System prompt template"; "save" is the dialog's button whose name contains "Save". Perform the steps in order. 1. Navigate to /c/{{SLUG}}, open the knob and edit tools. Click the browser tool's radio whose name contains "Off", then apply. The last marker should have the attribute aria-label exactly "Tools turned off: browser.". 2. The picker says what changed and offers to keep it: open the knob. The panel's data-testid "model-picker-tools" should contain the text "browser off", and changed the text "Differs from Default: browser off". 3. Save as new "E2E profile". The last marker should have the attribute aria-label exactly "Profile changed to E2E profile."; changed should be hidden; the profile "E2E profile" should have aria-checked="true". 4. Edit profiles, then click the button whose name contains "Edit" in the row "E2E profile". The dialog's ".modal-title" should have exactly the text "Profile: E2E profile". 5. Click the dialog's radio whose name contains "Custom". Fill the prompt with "Be brief in {{.WorkingDir}}." (those braces are text to type, not a placeholder). The prompt's accessible description (the text of the elements its aria-describedby names) should become exactly "Line 1: unknown variable .WorkingDir", and save should be disabled. 6. Fill the prompt with "Be brief in {{.WorkingDirectory}}.": save should be enabled. Click save: the dialog's ".modal-title" should have exactly the text "Profiles". Press Escape: the dialog should be hidden. 7. The conversation still has the old prompt; Revert takes the profile's: open the knob. Changed should contain the text "Differs from E2E profile: system prompt". Click changed's button whose name contains "Revert". The last marker should have the attribute aria-label exactly "System prompt changed."; changed should be hidden; the panel should be visible; the profile "E2E profile" should have keyboard focus; the last marker should contain the text "new system prompt". 8. Call "last system prompt": it must return "ok". There should be exactly 1 ".system-prompt-view" inside data-testid "generation-context". 9. Click the profile "Default". The last marker should have the attribute aria-label exactly "Profile changed to Default; tools turned on: browser; system prompt changed.", and there should still be exactly 1 ".system-prompt-view" inside data-testid "generation-context".`, func(s *shelley) {
		s.device(profilesDesktop)
		c := s.conversation("echo: profiles", "")
		s.v("SLUG", c.Slug)
		want := regexp.MustCompile(`^Be brief in /.+\.\n$`)
		s.call("last system prompt", func() string {
			var got struct {
				Messages []struct {
					Type    string `json:"type"`
					LLMData string `json:"llm_data"`
				} `json:"messages"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &got)
			text := "no system message"
			for _, m := range got.Messages {
				if m.Type != "system" {
					continue
				}
				var data struct {
					Content []struct {
						Text string `json:"Text"`
					} `json:"Content"`
				}
				json.Unmarshal([]byte(m.LLMData), &data)
				text = ""
				if len(data.Content) > 0 {
					text = data.Content[0].Text
				}
			}
			if want.MatchString(text) {
				return "ok"
			}
			return mustJSON(text)
		})
	})
}

func TestE2EProfilesDeletedProfileSavedAsNew(t *testing.T) {
	e2e(t, `Settings whose profile was deleted can be saved as a new one. The conversation /c/{{SLUG}} uses the profile "E2E gone". Call "settings tool overrides" returns the tool_overrides of the conversation's settings as JSON. `+profilesHow+`Perform the steps in order. 1. Navigate to /c/{{SLUG}}. 2. Turning on a tool that is on anyway changes nothing: open the knob; changed should be hidden. Edit tools, click the browser tool's radio named "On", then apply. Call "settings tool overrides" until it returns {"browser":"on"}. Open the knob: changed should be hidden. 3. Delete the profile in the profiles editor, which asks first: edit profiles. Click the button named "Delete" in the row "E2E gone", then that row's button whose name contains "Delete “E2E gone”". There should be no row "E2E gone" (count 0). Click the dialog's button whose name contains "Close modal". 4. Open the knob. Changed should contain the text "“E2E gone” was deleted", and changed should hold no button whose name contains "Revert" (count 0). 5. Save as new "E2E gone kept". The last marker should have the attribute aria-label exactly "Profile changed to E2E gone kept.", and changed should be hidden.`, func(s *shelley) {
		s.device(profilesDesktop)
		c := s.conversation("echo: orphaned", "")
		s.v("SLUG", c.Slug)
		s.api("POST", "/api/profiles", map[string]any{"name": "E2E gone"}, nil)
		s.api("POST", "/api/conversation/"+c.ID+"/settings", map[string]any{"profile": "E2E gone"}, nil)
		s.call("settings tool overrides", func() string { return mustJSON(profilesSettings(s, c.ID)["tool_overrides"]) })
	})
}

func TestE2EProfilesVoicePromptGeneratedEditedKept(t *testing.T) {
	e2e(t, `A profile's voice prompt is generated, edited and kept. The profile "E2E voice" has the model predictable and the system prompt "You are Ahab, a terse pirate." Call "generate input" returns the JSON body of the page's request to generate a voice prompt ("none" before one); call "stored voice prompt" returns the profile's stored voice prompt, JSON-encoded; call "save settings elsewhere" saves settings into the profile through the API, as another page would (model predictable, system prompt "You are Ahab."). `+profilesHow+`"The voice prompt" is the dialog's textbox named "Voice prompt". Perform the steps in order. 1. Navigate to /c/{{SLUG}}, open the knob and edit profiles. Click the button whose name contains "Edit" in the row "E2E voice". 2. The voice prompt should have the value "" (empty). 3. Generating asks the server, from the profile's model and system prompt, and fills the editor; nothing is saved until Save: click the dialog's button whose name contains "Generate from system prompt". Call "generate input" until it returns {"model":"predictable","system_prompt":"You are Ahab, a terse pirate."}. The voice prompt's value should become non-empty. Call "stored voice prompt": it must return "\"\"". 4. Fill the voice prompt with "You are Ahab. Speak like a pirate, briefly." and click the dialog's button whose name contains "Save". The dialog's ".modal-title" should have exactly the text "Profiles". Call "stored voice prompt" until it returns "\"You are Ahab. Speak like a pirate, briefly.\"". 5. Saving settings into the profile elsewhere keeps its voice prompt: call "save settings elsewhere", then call "stored voice prompt": it must still return "\"You are Ahab. Speak like a pirate, briefly.\"".`, func(s *shelley) {
		s.device(profilesDesktop)
		const name = "E2E voice"
		s.api("POST", "/api/profiles", map[string]any{"name": name, "model": "predictable", "system_prompt": "You are Ahab, a terse pirate."}, nil)
		s.v("SLUG", s.conversation("echo: voice prompt", "").Slug)
		var mu sync.Mutex
		input := "none"
		s.route("POST /api/profiles/voice-prompt/generate", func(w http.ResponseWriter, r *http.Request) {
			body := mustJSON(object(readBody(r)))
			mu.Lock()
			input = body
			mu.Unlock()
			s.pass.ServeHTTP(w, r)
		})
		s.call("generate input", func() string {
			mu.Lock()
			defer mu.Unlock()
			return input
		})
		s.call("stored voice prompt", func() string {
			var got []map[string]any
			s.api("GET", "/api/profiles", nil, &got)
			for _, p := range got {
				if p["name"] == name {
					return mustJSON(p["voice_prompt"])
				}
			}
			return "no profile"
		})
		s.call("save settings elsewhere", func() string {
			s.api("PUT", "/api/profiles/"+url.PathEscape(name), map[string]any{"model": "predictable", "system_prompt": "You are Ahab."}, nil)
			return "saved"
		})
	})
}

func TestE2EProfilesComposerProfileStartsConversation(t *testing.T) {
	e2e(t, `A profile picked in the composer starts the conversation. The profile "E2E composer" has the model predictable with the browser tool off. Call "started settings" returns the profile, model, thinking_level and tool_overrides of the settings of the conversation the page's first send started, as JSON ("none" before one). `+menusOpenModelPickerHow+profilesHow+`Perform the steps in order. 1. Navigate to /new and open the model picker. 2. Click the profile "E2E composer", then press Escape. 3. Fill data-testid "message-input" with "echo: from a profile" and click data-testid "send-button". 4. Call "started settings" until it returns {"model":"predictable","profile":"E2E composer","thinking_level":"","tool_overrides":{"browser":"off"}}.`, func(s *shelley) {
		s.device(profilesDesktop)
		s.api("POST", "/api/profiles", map[string]any{"name": "E2E composer", "model": "predictable", "tool_overrides": map[string]any{"browser": "off"}}, nil)
		profilesStarted(s, "profile", "model", "thinking_level", "tool_overrides")
	})
}

func TestE2EProfilesKeyboardSelectionScrollsMenu(t *testing.T) {
	e2e(t, `The profile menu's keyboard selection stays visible as it scrolls. The screen is a 1280x800 desktop window. There are ten profiles, "Scroll-0" to "Scroll-9". "The input" is data-testid "message-input"; "the menu" is data-testid "profile-menu"; "the selection" is the menu's element with role "option" and aria-selected="true". "The selection is in view" means the selection's bounding box lies within that of its closest [role="listbox"] ancestor (top >= the listbox's top and bottom <= its bottom), checked with an eval re-run until it holds. Perform the steps in order. 1. Navigate to /new. Fill the input with "+Scroll". 2. The menu should hold exactly 10 elements with role "option", and it must scroll: its scrollHeight must be greater than its clientHeight. 3. With the input focused, press ArrowDown 9 times. The selection should contain the text "Scroll-9", and the selection should be in view. 4. Press ArrowDown once more: the selection should contain "Scroll-0" and be in view. 5. Press Enter: the input should have the value exactly "+Scroll-0 " (with a trailing space).`, func(s *shelley) {
		s.device(profilesDesktop)
		for i := range 10 {
			s.api("POST", "/api/profiles", map[string]any{"name": "Scroll-" + strconv.Itoa(i)}, nil)
		}
	})
}
