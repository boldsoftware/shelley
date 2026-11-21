package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"testing"

	"shelley.exe.dev/db/generated"
)

func TestBuiltinProfilesUpgrade(t *testing.T) {
	db := setupDBMigratedThrough(t, 45)
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	syncBuiltinVersion(t, db, map[string]string{"Writer": `{"model":"v1"}`})
	writer := builtinProfile(t, db, "Writer")
	opts := ParseConversationOptions(writer.Settings)
	opts.Profile = "Writer"
	conversation, err := db.CreateConversation(t.Context(), nil, true, nil, nil, opts)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.QueriesTx(t.Context(), func(q *generated.Queries) error {
		if err := q.ClearDefaultProfile(t.Context()); err != nil {
			return err
		}
		_, err := q.SetDefaultProfile(t.Context(), "Writer")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	for _, version := range []string{"v2", "v3"} {
		settings := builtinSettings(t, writer.Settings, map[string]any{"system_prompt": version})
		syncBuiltinVersion(t, db, map[string]string{"Writer": settings, "Default": `{"model":"` + version + `"}`})
		if got := builtinProfile(t, db, "Writer"); got.Settings != settings || !got.IsDefault || got.UserEdited {
			t.Fatalf("%s: Writer did not upgrade while retaining default selection: %+v", version, got)
		}
		if got := builtinProfile(t, db, "Default"); got.Settings != `{"model":"`+version+`"}` || got.IsDefault {
			t.Fatalf("%s: no-op save froze Default or changed selection: %+v", version, got)
		}
		stored, err := db.GetConversationByID(t.Context(), conversation.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.ConversationOptions != conversation.ConversationOptions {
			t.Fatal("profile upgrade changed an existing conversation")
		}
	}
}

func TestBuiltinProfilesPreserveEditsUntilRestored(t *testing.T) {
	originalDB := setupTestDB(t)
	original := builtinProfile(t, originalDB, "Default").Settings
	for field, value := range map[string]any{
		"model": "my-model", "tool_overrides": map[string]string{"bash": "off"},
		"system_prompt": "My instructions", "voice_prompt": "Speak softly",
	} {
		t.Run(field, func(t *testing.T) {
			db := setupTestDB(t)
			edited := builtinSettings(t, original, map[string]any{field: value})
			updateBuiltinProfile(t, db, "Default", edited)
			for _, settings := range []string{`{"system_prompt":"v2"}`, `{"system_prompt":"v3"}`} {
				syncBuiltinVersion(t, db, map[string]string{"Default": settings})
				if got := builtinProfile(t, db, "Default"); got.Settings != edited {
					t.Fatalf("overwrote %s edit: %s", field, got.Settings)
				}
			}
			// Manually reverting an edit does not clear the user-edited flag.
			updateBuiltinProfile(t, db, "Default", original)
			syncBuiltinVersion(t, db, map[string]string{"Default": `{"model":"v4"}`})
			if got := builtinProfile(t, db, "Default"); got.Settings != original || !got.UserEdited {
				t.Fatal("manual edit unexpectedly re-enabled automatic updates")
			}
			if _, err := db.RestoreBuiltinProfile(t.Context(), "Default"); err != nil {
				t.Fatal(err)
			}
			syncBuiltinVersion(t, db, map[string]string{"Default": `{"model":"v5"}`})
			if got := builtinProfile(t, db, "Default"); got.Settings != `{"model":"v5"}` || got.UserEdited {
				t.Fatal("Restore defaults did not re-enable automatic updates")
			}
		})
	}
}

func TestBuiltinProfilesInstallCollisionAndDeletion(t *testing.T) {
	db := setupTestDB(t)
	if err := db.QueriesTx(t.Context(), func(q *generated.Queries) error {
		if _, err := q.CreateProfile(t.Context(), generated.CreateProfileParams{Name: "Exact", Settings: `{"model":"v1"}`}); err != nil {
			return err
		}
		_, err := q.CreateProfile(t.Context(), generated.CreateProfileParams{Name: "Research", Settings: `{"model":"mine"}`})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	syncBuiltinVersion(t, db, map[string]string{"Research": `{"model":"v1"}`, "Exact": `{"model":"v1"}`, "Writer": `{"model":"v1"}`})
	if got := builtinProfile(t, db, "Writer"); got.Settings != `{"model":"v1"}` || got.IsDefault {
		t.Fatalf("new builtin not installed as opt-in: %+v", got)
	}
	if err := db.QueriesTx(t.Context(), func(q *generated.Queries) error {
		_, err := q.DeleteProfile(t.Context(), "Writer")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"v2", "v3"} {
		syncBuiltinVersion(t, db, map[string]string{"Research": `{"model":"` + version + `"}`, "Exact": `{"model":"` + version + `"}`, "Writer": `{"model":"` + version + `"}`})
		if got := builtinProfile(t, db, "Research"); got.Settings != `{"model":"mine"}` {
			t.Fatalf("overwrote name collision: %+v", got)
		}
		if got := builtinProfile(t, db, "Exact"); got.Settings != `{"model":"v1"}` || got.Builtin {
			t.Fatalf("adopted a user-created profile with matching defaults: %+v", got)
		}
		if err := db.Queries(t.Context(), func(q *generated.Queries) error {
			_, err := q.GetProfile(t.Context(), "Writer")
			return err
		}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("deleted builtin restored: %v", err)
		}
	}
}

func builtinProfile(t *testing.T, db *DB, name string) (profile generated.Profile) {
	t.Helper()
	if err := db.Queries(t.Context(), func(q *generated.Queries) error {
		var err error
		profile, err = q.GetProfile(t.Context(), name)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return profile
}

func updateBuiltinProfile(t *testing.T, db *DB, name, settings string) {
	t.Helper()
	if err := db.QueriesTx(t.Context(), func(q *generated.Queries) error {
		_, err := q.UpdateProfile(t.Context(), generated.UpdateProfileParams{Name: name, Settings: settings})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func syncBuiltinVersion(t *testing.T, db *DB, profiles map[string]string) {
	t.Helper()
	if err := db.syncBuiltinProfiles(t.Context(), profiles); err != nil {
		t.Fatal(err)
	}
}

func builtinSettings(t *testing.T, original string, edits map[string]any) string {
	t.Helper()
	var settings map[string]any
	if err := json.Unmarshal([]byte(original), &settings); err != nil {
		t.Fatal(err)
	}
	for key, value := range edits {
		settings[key] = value
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// Assistant is installed opt-in, with its idle policy and guidance.
func TestBuiltinAssistant(t *testing.T) {
	db := setupTestDB(t)
	assistant := builtinProfile(t, db, "Assistant")
	if !assistant.Builtin || assistant.UserEdited || assistant.IsDefault {
		t.Fatalf("Assistant: %+v", assistant)
	}
	opts := ParseConversationOptions(assistant.Settings)
	if opts.IdleCompactionPrompt != assistantIdlePrompt || opts.IdleCompactionMinutes != 8 || opts.CacheKeepWarmMinutes != 8 {
		t.Fatalf("Assistant idle policy: %+v", opts)
	}
}

// A user's own Assistant from before it shipped is kept, not adopted.
func TestBuiltinAssistantKeepsUserProfile(t *testing.T) {
	db := setupDBMigratedThrough(t, 45)
	t.Cleanup(func() { db.Close() })
	mine := `{"model":"custom-assistant","system_prompt":"Keep my instructions"}`
	if err := db.QueriesTx(t.Context(), func(q *generated.Queries) error {
		_, err := q.CreateProfile(t.Context(), generated.CreateProfileParams{Name: "Assistant", Settings: mine})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The user's settings stay, with the tools the Assistant answers with on.
	got := builtinProfile(t, db, "Assistant")
	var settings struct {
		Model         string            `json:"model"`
		SystemPrompt  string            `json:"system_prompt"`
		ToolOverrides map[string]string `json:"tool_overrides"`
	}
	if err := json.Unmarshal([]byte(got.Settings), &settings); err != nil {
		t.Fatal(err)
	}
	if got.Builtin || settings.Model != "custom-assistant" || settings.SystemPrompt != "Keep my instructions" ||
		!maps.Equal(settings.ToolOverrides, WithAssistantTools(nil)) {
		t.Fatalf("user's Assistant changed: %+v", got)
	}
}

// Slack events now go to the Assistant: the Slack Assistant's conversation
// is archived with its history and no Slack chat, and its profile goes
// unless the user edited it.
func TestSlackAssistantMigration(t *testing.T) {
	t.Parallel()
	for _, edited := range []bool{false, true} {
		db := setupDBMigratedThrough(t, 82)
		ctx := t.Context()
		if err := db.pool.Exec(ctx, `INSERT INTO profiles(name, settings, builtin, user_edited) VALUES('Slack Assistant', '{}', TRUE, ?);
			INSERT INTO builtin_profiles(name) VALUES('Slack Assistant');
			INSERT INTO conversations(conversation_id, slug, user_initiated, external_conversation_id, external_endpoint, conversation_options, agent_working)
			VALUES('s', 'slack-app', 1, 'slack:assistant', 'https://slack-app.int.exe.xyz', '{"profile":"Slack Assistant"}', 1)`, edited); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		c, err := db.GetConversationByID(ctx, "s")
		if err != nil {
			t.Fatal(err)
		}
		if c.ExternalConversationID != nil || c.ExternalEndpoint != nil || !c.Archived || c.AgentWorking || ptrValue(c.Slug) != "slack-app" {
			t.Fatalf("edited=%v: Slack Assistant conversation: %+v", edited, c)
		}
		var profile generated.Profile
		err = db.Queries(ctx, func(q *generated.Queries) (err error) {
			if _, err := q.GetBuiltinProfile(ctx, "Slack Assistant"); !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("still registered: %v", err)
			}
			profile, err = q.GetProfile(ctx, "Slack Assistant")
			return err
		})
		if edited && (err != nil || profile.Builtin) {
			t.Fatalf("edited profile: %+v, %v", profile, err)
		}
		if !edited && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unedited profile: %+v, %v", profile, err)
		}
		db.Close()
	}
}
