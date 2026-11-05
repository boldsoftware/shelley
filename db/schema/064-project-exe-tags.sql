-- The exe.dev tag through which integrations added in Edit project reach a
-- project's VMs: chosen from its name when first used and kept across
-- renames, so the attachments made to it keep working.
CREATE TABLE profile_exe_tags (
    profile TEXT PRIMARY KEY REFERENCES profiles(name) ON UPDATE CASCADE ON DELETE CASCADE,
    tag TEXT NOT NULL UNIQUE
);
