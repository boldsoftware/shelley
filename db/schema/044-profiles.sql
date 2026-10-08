-- Profiles are named conversation settings (model, reasoning, tools, system
-- prompt) that new conversations start from and existing ones can switch to.
-- settings is a JSON object; see server.Settings. Exactly one profile is the
-- default, the one used when a new conversation names none.
CREATE TABLE profiles (
    name TEXT PRIMARY KEY,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    settings TEXT NOT NULL DEFAULT '{}'
);

CREATE UNIQUE INDEX profiles_one_default ON profiles(is_default) WHERE is_default;

-- Empty settings: the server's default model, the model's default reasoning,
-- default tools, and the built-in system prompt.
INSERT INTO profiles (name, is_default) VALUES ('Default', TRUE);
