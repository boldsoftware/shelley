-- Quick replies the composer offers beside next_prompt_suggestion: a JSON
-- array of short replies, in preference order, from the same model call and
-- under the same next_prompt_suggestion_seq.
ALTER TABLE conversations ADD COLUMN next_prompt_quick_replies TEXT NOT NULL DEFAULT '[]';
