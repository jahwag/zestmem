package migrations

import "testing"

func TestLoadIncludesOnlyOrderedForwardMigrations(t *testing.T) {
	loaded, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("migration count=%d, want 2", len(loaded))
	}
	if loaded[0].version != 1 || loaded[0].name != "init" {
		t.Fatalf("first migration=%+v", loaded[0])
	}
	if loaded[1].version != 2 || loaded[1].name != "authorization" {
		t.Fatalf("second migration=%+v", loaded[1])
	}
	for _, migration := range loaded {
		if len(migration.checksum) != 64 {
			t.Fatalf("migration %d checksum length=%d", migration.version, len(migration.checksum))
		}
	}
}
