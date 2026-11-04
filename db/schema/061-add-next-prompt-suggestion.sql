-- A suggested next user message, generated after a turn ends. The UI shows
-- next_prompt_suggestion in the empty composer while nothing but usage
-- markers and git and model notes follows the turn's end-of-turn message
-- (next_prompt_suggestion_seq). Sequence ids start at 1, so 0 means none.
ALTER TABLE conversations ADD COLUMN next_prompt_suggestion TEXT NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN next_prompt_suggestion_seq INTEGER NOT NULL DEFAULT 0;
