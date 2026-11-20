package db

import (
	"encoding/json"
	"maps"
	"testing"

	"shelley.exe.dev/db/generated"
)

// Settings that turned output_iframe off turn its successor, output_html, off
// too, unless they say otherwise.
func TestOutputHTMLMigration(t *testing.T) {
	t.Parallel()
	db := setupDBMigratedThrough(t, 84)
	defer db.Close()
	ctx := t.Context()
	cases := map[string]struct{ before, after string }{
		"off":     {`{"tool_overrides":{"output_iframe":"off"}}`, `{"output_iframe":"off","output_html":"off"}`},
		"html on": {`{"tool_overrides":{"output_iframe":"off","output_html":"on"}}`, `{"output_iframe":"off","output_html":"on"}`},
		"on":      {`{"tool_overrides":{"output_iframe":"on"}}`, `{"output_iframe":"on"}`},
		"none":    {`{}`, `null`},
	}
	// Malformed JSON stays as it is.
	if err := db.pool.Exec(ctx, `INSERT INTO profiles(name, settings) VALUES('malformed', '{')`); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.Exec(ctx, `INSERT INTO conversations(conversation_id, slug, user_initiated, conversation_options)
		VALUES('malformed', 'malformed', 1, '{')`); err != nil {
		t.Fatal(err)
	}
	for name, c := range cases {
		if err := db.pool.Exec(ctx, `INSERT INTO profiles(name, settings) VALUES(?, ?)`, name, c.before); err != nil {
			t.Fatal(err)
		}
		if err := db.pool.Exec(ctx, `INSERT INTO conversations(conversation_id, slug, user_initiated, conversation_options)
			VALUES(?, ?, 1, ?)`, name, name, c.before); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for name, c := range cases {
		var want map[string]string
		if err := json.Unmarshal([]byte(c.after), &want); err != nil {
			t.Fatal(err)
		}
		var profile generated.Profile
		if err := db.Queries(ctx, func(q *generated.Queries) (err error) {
			profile, err = q.GetProfile(ctx, name)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var settings struct {
			ToolOverrides map[string]string `json:"tool_overrides"`
		}
		if err := json.Unmarshal([]byte(profile.Settings), &settings); err != nil {
			t.Fatal(err)
		}
		conv, err := db.GetConversationByID(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		var opts ConversationOptions
		if err := json.Unmarshal([]byte(conv.ConversationOptions), &opts); err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(settings.ToolOverrides, want) || !maps.Equal(opts.ToolOverrides, want) {
			t.Errorf("%s: profile %v, conversation %v, want %v", name, settings.ToolOverrides, opts.ToolOverrides, want)
		}
	}
}
