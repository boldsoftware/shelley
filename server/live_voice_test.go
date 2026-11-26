package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestLiveVoiceAPI(t *testing.T) {
	t.Parallel()
	e := newProfileEnv(t)

	var resp liveVoiceResponse
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/live-voice", "")), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Voice != defaultLiveVoice || len(resp.Voices) != 38 || !slices.Contains(resp.Voices, defaultLiveVoice) {
		t.Fatalf("initial GET = %+v", resp)
	}

	// Unknown fields, invalid JSON, and unknown voices are rejected without changing state.
	for _, bad := range []string{
		`{"voice":"nope"}`,
		`{"voice":"cedar","extra":1}`,
		`not json`,
	} {
		e.want(400, "PUT", "/api/live-voice", bad)
	}
	e.want(405, "POST", "/api/live-voice", `{"voice":"cedar"}`)

	// Valid voices trim whitespace and persist.
	if err := json.Unmarshal([]byte(e.want(200, "PUT", "/api/live-voice", `{"voice":" cedar "}`)), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Voice != "cedar" {
		t.Fatalf("PUT cedar = %+v", resp)
	}
	if err := json.Unmarshal([]byte(e.want(200, "GET", "/api/live-voice", "")), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Voice != "cedar" {
		t.Fatalf("GET after PUT cedar = %+v", resp)
	}
	if stored, err := e.database.GetSetting(t.Context(), liveVoiceSettingKey); err != nil || stored != "cedar" {
		t.Fatalf("stored = %q, err = %v", stored, err)
	}

	// Selecting the default or clearing resets to marin and removes the DB override.
	if err := json.Unmarshal([]byte(e.want(200, "PUT", "/api/live-voice", `{"voice":"alloy"}`)), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Voice != "alloy" {
		t.Fatalf("PUT alloy = %+v", resp)
	}
	if err := json.Unmarshal([]byte(e.want(200, "PUT", "/api/live-voice", `{"voice":"marin"}`)), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Voice != defaultLiveVoice {
		t.Fatalf("PUT marin = %+v", resp)
	}
	if stored, err := e.database.GetSetting(t.Context(), liveVoiceSettingKey); err != nil || stored != "" {
		t.Fatalf("stored after marin = %q, err = %v", stored, err)
	}

	e.want(200, "PUT", "/api/live-voice", `{"voice":"alloy"}`)
	if err := json.Unmarshal([]byte(e.want(200, "PUT", "/api/live-voice", `{"voice":""}`)), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Voice != defaultLiveVoice {
		t.Fatalf("PUT empty = %+v", resp)
	}
	if stored, err := e.database.GetSetting(t.Context(), liveVoiceSettingKey); err != nil || stored != "" {
		t.Fatalf("stored after empty = %q, err = %v", stored, err)
	}
}

func TestIndexIncludesLiveVoice(t *testing.T) {
	t.Parallel()
	srv, database, _ := newTestServer(t)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	readInit := func() map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET / = %d, want 200: %s", w.Code, w.Body.String())
		}
		const prefix = "window.__SHELLEY_INIT__="
		body := w.Body.String()
		_, after, ok := strings.Cut(body, prefix)
		if !ok {
			t.Fatal("missing __SHELLEY_INIT__")
		}
		raw, _, ok := strings.Cut(after, ";</script>")
		if !ok {
			t.Fatal("malformed __SHELLEY_INIT__")
		}
		var init map[string]any
		if err := json.Unmarshal([]byte(raw), &init); err != nil {
			t.Fatal(err)
		}
		return init
	}

	if got := readInit()["live_voice"]; got != defaultLiveVoice {
		t.Fatalf("default init live_voice = %v, want %q", got, defaultLiveVoice)
	}
	if err := database.SetSetting(t.Context(), liveVoiceSettingKey, "cedar"); err != nil {
		t.Fatal(err)
	}
	if got := readInit()["live_voice"]; got != "cedar" {
		t.Fatalf("updated init live_voice = %v, want cedar", got)
	}
}
