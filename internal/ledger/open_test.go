package ledger

import (
	"path/filepath"
	"testing"
)

func TestIsPostgresDSN(t *testing.T) {
	cases := []struct {
		dsn  string
		want bool
	}{
		{"postgres://user:pass@host:5432/db", true},
		{"postgresql://host/db", true},
		{"/home/user/.leash/leash.db", false},
		{"leash.db", false},
		{"./relative.db", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isPostgresDSN(c.dsn); got != c.want {
			t.Fatalf("isPostgresDSN(%q) = %v, want %v", c.dsn, got, c.want)
		}
	}
}

func TestOpenSQLitePathStillWorks(t *testing.T) {
	// A plain path must continue to open the SQLite backend after the DSN
	// dispatch was added.
	l, err := Open(filepath.Join(t.TempDir(), "leash.db"))
	if err != nil {
		t.Fatalf("Open sqlite path: %v", err)
	}
	defer l.Close()
}

func TestOpenPostgresUnreachableRecoversPanic(t *testing.T) {
	// The postgres backend panics when it cannot reach the database (its New
	// runs CREATE TABLE eagerly). Open must recover that into an ordinary error,
	// never let a panic escape. Reaching a closed port makes it fail promptly.
	l, err := Open("postgres://nobody:nobody@127.0.0.1:1/nodb?sslmode=disable&connect_timeout=1")
	if err == nil {
		_ = l.Close()
		t.Fatalf("Open on an unreachable postgres returned no error")
	}
}
