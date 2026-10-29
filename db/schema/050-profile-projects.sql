-- A project is a profile with instructions and a working directory. A
-- conversation belongs to the project its profile label names (see
-- db/projects.go); deleting the profile deletes the project.
CREATE TABLE profile_projects (
    profile TEXT PRIMARY KEY REFERENCES profiles(name) ON UPDATE CASCADE ON DELETE CASCADE,
    instructions TEXT NOT NULL DEFAULT '',
    cwd TEXT NOT NULL DEFAULT ''
);
