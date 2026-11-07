-- The name and email a project's tasks commit as, overriding the default
-- commit identity (setting commit_identity); a project without a row uses
-- the default.
CREATE TABLE profile_commit_identities (
    profile TEXT PRIMARY KEY REFERENCES profiles(name) ON UPDATE CASCADE ON DELETE CASCADE,
    name TEXT NOT NULL,
    email TEXT NOT NULL
);
