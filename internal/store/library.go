package store

import "context"

const libraryTable = `CREATE TABLE library_files (
  path         TEXT    PRIMARY KEY,
  size         INTEGER NOT NULL,
  mod_time     INTEGER NOT NULL,
  sha256       TEXT    NOT NULL,
  title        TEXT    NOT NULL DEFAULT '',
  author       TEXT    NOT NULL DEFAULT '',
  series       TEXT    NOT NULL DEFAULT '',
  series_index REAL    NOT NULL DEFAULT 0,
  unreadable   TEXT    NOT NULL DEFAULT ''
);`

// LibraryFile is what a file in the library held when it last had this
// size and modification time (in Unix nanoseconds).
type LibraryFile struct {
	Path        string
	Size        int64
	ModTime     int64
	SHA256      string
	Title       string
	Author      string
	Series      string
	SeriesIndex float64
	Unreadable  string
}

// LibraryFiles returns every cached file, by path.
func (s *Store) LibraryFiles(ctx context.Context) (map[string]LibraryFile, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT path, size, mod_time, sha256, title, author, series, series_index, unreadable FROM library_files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LibraryFile{}
	for rows.Next() {
		var f LibraryFile
		if err := rows.Scan(&f.Path, &f.Size, &f.ModTime, &f.SHA256, &f.Title, &f.Author, &f.Series, &f.SeriesIndex, &f.Unreadable); err != nil {
			return nil, err
		}
		out[f.Path] = f
	}
	return out, rows.Err()
}

// PutLibraryFile remembers what f holds, replacing what was cached for
// its path.
func (s *Store) PutLibraryFile(ctx context.Context, f LibraryFile) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO library_files (path, size, mod_time, sha256, title, author, series, series_index, unreadable)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET size = excluded.size, mod_time = excluded.mod_time, sha256 = excluded.sha256, title = excluded.title,
		  author = excluded.author, series = excluded.series, series_index = excluded.series_index, unreadable = excluded.unreadable`,
		f.Path, f.Size, f.ModTime, f.SHA256, f.Title, f.Author, f.Series, f.SeriesIndex, f.Unreadable)
	return err
}

// DeleteLibraryFile forgets a file that's no longer in the library.
func (s *Store) DeleteLibraryFile(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM library_files WHERE path = ?", path)
	return err
}
