package migrations

import "testing"

func TestLoadIncludesOnlyOrderedForwardMigrations(t *testing.T) {
	loaded, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 6 {
		t.Fatalf("migration count=%d, want 6", len(loaded))
	}
	if loaded[0].version != 1 || loaded[0].name != "init" {
		t.Fatalf("first migration=%+v", loaded[0])
	}
	if loaded[1].version != 2 || loaded[1].name != "authorization" {
		t.Fatalf("second migration=%+v", loaded[1])
	}
	if loaded[2].version != 3 || loaded[2].name != "browse_recency" {
		t.Fatalf("third migration=%+v", loaded[2])
	}
	if loaded[3].version != 4 || loaded[3].name != "memory_principal_assertions" {
		t.Fatalf("fourth migration=%+v", loaded[3])
	}
	if loaded[4].version != 5 || loaded[4].name != "use_when" {
		t.Fatalf("fifth migration=%+v", loaded[4])
	}
	if loaded[5].version != 6 || loaded[5].name != "use_when_validation" {
		t.Fatalf("sixth migration=%+v", loaded[5])
	}
	for _, migration := range loaded {
		if len(migration.checksum) != 64 {
			t.Fatalf("migration %d checksum length=%d", migration.version, len(migration.checksum))
		}
	}
}
