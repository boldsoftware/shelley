-- GitHub repositories attached to a project through exe.dev GitHub
-- integrations, in the order chosen.
CREATE TABLE profile_repos (
    profile TEXT NOT NULL REFERENCES profiles(name) ON UPDATE CASCADE ON DELETE CASCADE,
    position INTEGER NOT NULL,
    integration TEXT NOT NULL,
    repository TEXT NOT NULL,
    PRIMARY KEY (profile, position)
);
