package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"maps"

	"shelley.exe.dev/db/generated"
)

//go:embed assistant_system_prompt.txt
var assistantSystemPrompt string

//go:embed assistant_idle_prompt.txt
var assistantIdlePrompt string

// AssistantProfile names the shipped Assistant profile. The most recently
// created top-level conversation with it is the Assistant (see
// 088-assistant-profile.sql), whether or not the profile still exists.
const AssistantProfile = "Assistant"

// AssistantTools are the tools the Assistant answers with: message_user for
// texts and the Slack tools for Slack. They are off by default and on in
// the Assistant profile. A conversation that becomes an Assistant without
// taking the profile's settings gets them on (see LabelAssistant and
// server.newConversationSettings).
var AssistantTools = []string{"message_user", "send_to_thread", "send_to_channel", "add_reaction"}

// WithAssistantTools returns a copy of overrides with AssistantTools on.
func WithAssistantTools(overrides map[string]string) map[string]string {
	out := make(map[string]string, len(overrides)+len(AssistantTools))
	maps.Copy(out, overrides)
	for _, name := range AssistantTools {
		out[name] = "on"
	}
	return out
}

// builtinProfiles is the single source of shipped profile settings.
var builtinProfiles = map[string]string{
	"Default":        "{}",
	AssistantProfile: assistantProfile(),
}

// assistantProfile is a chat-first assistant that talks through
// message_user, and in Slack through the Slack tools.
func assistantProfile() string {
	overrides := WithAssistantTools(map[string]string{
		"compact_in_place": "on", // idle compaction needs it, whatever the default
		"llm_one_shot":     "off",
		"output_html":      "off",
		"output_iframe":    "off",
	})
	settings, err := json.Marshal(map[string]any{
		"system_prompt":        assistantSystemPrompt,
		"compact_nudge_tokens": 130_000,
		// Quiet chats are compacted in place, keeping what matters about
		// the user; a warm cache keeps a quick follow-up cheap until then.
		"idle_compaction_minutes": 8,
		"idle_compaction_prompt":  assistantIdlePrompt,
		"cache_keep_warm_minutes": 8,
		"tool_overrides":          overrides,
		// It talks to the user through message_user; its turn ends are
		// nothing to announce.
		"disable_notifications": true,
	})
	if err != nil {
		panic(err)
	}
	return string(settings)
}

func (db *DB) updateBuiltinProfiles(ctx context.Context) error {
	return db.syncBuiltinProfiles(ctx, builtinProfiles)
}

// syncBuiltinProfiles installs each built-in once and updates only those the
// user has not edited. Registration survives deletion; updates never recreate it.
func (db *DB) syncBuiltinProfiles(ctx context.Context, profiles map[string]string) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		for name, settings := range profiles {
			_, err := q.GetBuiltinProfile(ctx, name)
			if errors.Is(err, sql.ErrNoRows) {
				if err := q.CreateBuiltinProfile(ctx, generated.CreateBuiltinProfileParams{Name: name, Settings: settings}); err != nil {
					return err
				}
				if err := q.RegisterBuiltinProfile(ctx, name); err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if err := q.UpdateBuiltinProfile(ctx, generated.UpdateBuiltinProfileParams{Name: name, Settings: settings}); err != nil {
				return err
			}
		}
		return nil
	})
}

// RestoreBuiltinProfile resets a built-in to its current shipped settings and
// re-enables automatic updates without changing default selection or chats.
func (db *DB) RestoreBuiltinProfile(ctx context.Context, name string) (*generated.Profile, error) {
	settings, ok := builtinProfiles[name]
	if !ok {
		return nil, sql.ErrNoRows
	}
	var profile generated.Profile
	err := db.QueriesTx(ctx, func(q *generated.Queries) (err error) {
		profile, err = q.RestoreBuiltinProfile(ctx, generated.RestoreBuiltinProfileParams{Name: name, Settings: settings})
		return err
	})
	return &profile, err
}
