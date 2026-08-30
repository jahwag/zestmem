package migrations

import "testing"

func TestLoadIncludesOnlyOrderedForwardMigrations(t *testing.T) {
	loaded, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 10 {
		t.Fatalf("migration count=%d, want 10", len(loaded))
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
	if loaded[6].version != 7 || loaded[6].name != "space_use_when" {
		t.Fatalf("seventh migration=%+v", loaded[6])
	}
	if loaded[7].version != 8 || loaded[7].name != "content_updated_at" {
		t.Fatalf("eighth migration=%+v", loaded[7])
	}
	if loaded[8].version != 9 || loaded[8].name != "active_direct_roles" {
		t.Fatalf("ninth migration=%+v", loaded[8])
	}
	if loaded[9].version != 10 || loaded[9].name != "retrieval_observability" {
		t.Fatalf("tenth migration=%+v", loaded[9])
	}
	for _, migration := range loaded {
		if len(migration.checksum) != 64 {
			t.Fatalf("migration %d checksum length=%d", migration.version, len(migration.checksum))
		}
	}
}
