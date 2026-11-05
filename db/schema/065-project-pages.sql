-- The web page a project's view opens on (its Page tab), e.g. the site its
-- tasks work on; a project without a row has none.
CREATE TABLE profile_pages (
    profile TEXT PRIMARY KEY REFERENCES profiles(name) ON UPDATE CASCADE ON DELETE CASCADE,
    url TEXT NOT NULL
);
