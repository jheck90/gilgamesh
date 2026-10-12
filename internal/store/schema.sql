-- Positions in a book are sentence ordinals ("ord"), counted from 0 in
-- reading order. Everything a reader can see is filtered by comparing a
-- revealed/first ord against their position, so every row that holds story
-- information carries one.

CREATE TABLE users (
  id         INTEGER PRIMARY KEY,
  name       TEXT    NOT NULL UNIQUE,
  is_admin   INTEGER NOT NULL DEFAULT 0,
  created_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE series (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE books (
  id           INTEGER PRIMARY KEY,
  series_id    INTEGER NOT NULL REFERENCES series(id),
  series_index REAL    NOT NULL DEFAULT 1,
  title        TEXT    NOT NULL,
  author       TEXT    NOT NULL DEFAULT '',
  source_path  TEXT    NOT NULL,
  sha256       TEXT    NOT NULL UNIQUE,
  created_at   TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE chapters (
  book_id   INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  idx       INTEGER NOT NULL, -- 1-based reading order
  title     TEXT    NOT NULL,
  first_ord INTEGER NOT NULL,
  last_ord  INTEGER NOT NULL,
  PRIMARY KEY (book_id, idx)
);

CREATE TABLE sentences (
  book_id     INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  ord         INTEGER NOT NULL,
  chapter_idx INTEGER NOT NULL,
  para        INTEGER NOT NULL, -- paragraph number within the book
  text        TEXT    NOT NULL,
  PRIMARY KEY (book_id, ord)
);

-- Entities belong to a series so names carry across books.
CREATE TABLE entities (
  id            INTEGER PRIMARY KEY,
  series_id     INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
  name          TEXT    NOT NULL, -- the first name the reader learns
  type          TEXT    NOT NULL,
  narrator      INTEGER NOT NULL DEFAULT 0,
  first_book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  first_ord     INTEGER NOT NULL,
  -- An alias to show as the entry's title once the reader reaches it,
  -- when the first name learned isn't the one it's best known by.
  title_alias_id INTEGER REFERENCES aliases(id) ON DELETE SET NULL
);
CREATE INDEX entities_series ON entities(series_id);

-- A link between a name and an entity can itself be a reveal, so aliases
-- carry their own position.
CREATE TABLE aliases (
  id           INTEGER PRIMARY KEY,
  entity_id    INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  alias        TEXT    NOT NULL,
  book_id      INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  revealed_ord INTEGER NOT NULL
);
CREATE INDEX aliases_entity ON aliases(entity_id);

CREATE TABLE facts (
  id           INTEGER PRIMARY KEY,
  entity_id    INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  text         TEXT    NOT NULL,
  book_id      INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  revealed_ord INTEGER NOT NULL, -- the last sentence the fact relies on
  evidence     TEXT    NOT NULL  -- JSON array of sentence ords
);
CREATE INDEX facts_entity ON facts(entity_id, book_id, revealed_ord);

CREATE TABLE time_markers (
  id      INTEGER PRIMARY KEY,
  book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  label   TEXT    NOT NULL,
  ord     INTEGER NOT NULL
);

-- Extraction runs chunk by chunk in reading order and can resume.
CREATE TABLE ingest_progress (
  book_id      INTEGER PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
  next_chunk   INTEGER NOT NULL DEFAULT 0,
  total_chunks INTEGER NOT NULL,
  chunk_words  INTEGER NOT NULL
);

-- Per-reader state.
CREATE TABLE positions (
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  book_id    INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  ord        INTEGER NOT NULL,
  updated_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (user_id, book_id)
);

-- Set only by an explicit "I finished this book", never by a snap. Reread
-- hints read from here, so a bad page match can't unlock them.
CREATE TABLE finished_books (
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  book_id     INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  finished_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (user_id, book_id)
);

-- A reader's physical edition, and printed page numbers mapped onto it.
CREATE TABLE copies (
  id      INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  label   TEXT    NOT NULL
);

CREATE TABLE page_anchors (
  copy_id      INTEGER NOT NULL REFERENCES copies(id) ON DELETE CASCADE,
  printed_page INTEGER NOT NULL,
  ord          INTEGER NOT NULL,
  PRIMARY KEY (copy_id, printed_page)
);

-- Bring your own key. Keys are sealed with the instance secret.
CREATE TABLE api_keys (
  user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  provider   TEXT    NOT NULL,
  base_url   TEXT    NOT NULL DEFAULT '',
  model      TEXT    NOT NULL DEFAULT '',
  sealed     BLOB    NOT NULL,
  created_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Requests to spend the admin's key.
CREATE TABLE key_grants (
  user_id           INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  status            TEXT    NOT NULL CHECK (status IN ('requested', 'approved', 'denied', 'revoked')),
  monthly_token_cap INTEGER NOT NULL DEFAULT 0, -- 0 means no cap
  note              TEXT    NOT NULL DEFAULT '',
  requested_at      TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  decided_at        TEXT,
  decided_by        INTEGER REFERENCES users(id)
);

-- Each applied gmesh edit. Another edit trims again, so it has to be asked for.
CREATE TABLE edits (
  id         INTEGER PRIMARY KEY,
  book_id    INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  model      TEXT    NOT NULL,
  created_at TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE usage (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key_owner_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  book_id       INTEGER REFERENCES books(id) ON DELETE SET NULL,
  provider      TEXT    NOT NULL,
  model         TEXT    NOT NULL,
  input_tokens  INTEGER NOT NULL,
  output_tokens INTEGER NOT NULL,
  kind          TEXT    NOT NULL DEFAULT 'ingest', -- 'ingest' reads the book, 'edit' tidies its notes
  effort        TEXT    NOT NULL DEFAULT '',       -- Anthropic effort; '' for none or not recorded
  created_at    TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX usage_user ON usage(user_id, created_at);
