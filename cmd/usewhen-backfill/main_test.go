package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestRequiresExactHashPrivateModeAndCompleteIdentity(t *testing.T) {
	item := entry{
		MemoryID: "00000000-0000-0000-0000-000000000002", ExpectedRevision: 1,
		ContentHash: strings.Repeat("a", 64), Title: "Reviewed memory",
		UseWhen: "Investigating reviewed memory behavior.", EvidenceRule: "body-supported", Reviewer: "sol-a",
	}
	plan := manifest{
		Format: manifestFormat, SpaceID: "00000000-0000-0000-0000-000000000001",
		SourceSnapshotSHA256: strings.Repeat("b", 64), Entries: []entry{item},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	loaded, actual, err := loadManifest(path, hash, 1)
	if err != nil || actual != hash || len(loaded.Entries) != 1 {
		t.Fatalf("loaded=%#v actual=%q error=%v", loaded, actual, err)
	}
	if _, _, err := loadManifest(path, strings.Repeat("0", 64), 1); err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("hash mismatch error=%v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadManifest(path, hash, 1); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("permission error=%v", err)
	}
}

func TestLoadManifestRejectsMalformedSourceAndCue(t *testing.T) {
	plan := manifest{
		Format: manifestFormat, SpaceID: "00000000-0000-0000-0000-000000000001",
		SourceSnapshotSHA256: "not-a-hash",
		Entries: []entry{{
			MemoryID: "00000000-0000-0000-0000-000000000002", ExpectedRevision: 1,
			ContentHash: strings.Repeat("a", 64), Title: "Reviewed memory",
			UseWhen: "unsafe\ncue", EvidenceRule: "body-supported", Reviewer: "sol-a",
		}},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if _, _, err := loadManifest(path, hex.EncodeToString(sum[:]), 1); err == nil {
		t.Fatal("malformed manifest was accepted")
	}
	plan.SourceSnapshotSHA256 = strings.Repeat("b", 64)
	plan.Entries[0].UseWhen = "Investigating a mechanically truncated cue..."
	raw, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(raw)
	if _, _, err := loadManifest(path, hex.EncodeToString(sum[:]), 1); err == nil || !strings.Contains(err.Error(), "non-truncated") {
		t.Fatalf("ASCII truncation error=%v", err)
	}
}

func TestBackfillIdentityAndIdempotencyKeyBindExactReviewedInput(t *testing.T) {
	item := entry{MemoryID: "00000000-0000-0000-0000-000000000002", ExpectedRevision: 1, ContentHash: strings.Repeat("a", 64), Title: "Reviewed memory", UseWhen: "Investigating reviewed memory behavior."}
	current := readResult{ID: item.MemoryID, SpaceID: "00000000-0000-0000-0000-000000000001"}
	current.CurrentRevision.Number = 1
	current.CurrentRevision.Title = item.Title
	current.CurrentRevision.ContentHash = item.ContentHash
	if err := verifySource(current.SpaceID, item, current); err != nil {
		t.Fatal(err)
	}
	key := idempotencyKey(strings.Repeat("b", 64), item)
	if key != idempotencyKey(strings.Repeat("b", 64), item) {
		t.Fatal("idempotency key is unstable")
	}
	changed := item
	changed.UseWhen += " Changed."
	if key == idempotencyKey(strings.Repeat("b", 64), changed) {
		t.Fatal("idempotency key did not bind cue")
	}
	current.CurrentRevision.Title = "Changed title"
	if err := verifySource(current.SpaceID, item, current); err == nil {
		t.Fatal("changed source identity was accepted")
	}
}
