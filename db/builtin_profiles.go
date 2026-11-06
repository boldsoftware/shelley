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

//go:embed slack_assistant_system_prompt.txt
var slackAssistantSystemPrompt string

// builtinProfiles is the single source of shipped profile settings.
var builtinProfiles = map[string]string{
	"Default":   "{}",
	"Assistant": assistantProfile(assistantSystemPrompt, map[string]string{"message_user": "on"}),
	// The Slack Assistant answers Slack messages delivered by exe.dev's Slack app.
	"Slack Assistant": assistantProfile(slackAssistantSystemPrompt, map[string]string{
		"send_to_thread":  "on",
		"send_to_channel": "on",
		"add_reaction":    "on",
		"message_user":    "off",
	}),
}

// assistantProfile is a chat-first assistant, prompted by systemPrompt,
// that talks through tools.
func assistantProfile(systemPrompt string, tools map[string]string) string {
	overrides := map[string]string{
		"compact_in_place": "on", // idle compaction needs it, whatever the default
		"llm_one_shot":     "off",
		"output_iframe":    "off",
	}
	maps.Copy(overrides, tools)
	settings, err := json.Marshal(map[string]any{
		"system_prompt":        systemPrompt,
		"compact_nudge_tokens": 130_000,
		// Quiet chats are compacted in place, keeping what matters about
		// the user; a warm cache keeps a quick follow-up cheap until then.
		"idle_compaction_minutes": 8,
		"idle_compaction_prompt":  assistantIdlePrompt,
		"cache_keep_warm_minutes": 8,
		"tool_overrides":          overrides,
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
