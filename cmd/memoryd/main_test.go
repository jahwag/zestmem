package main

import "testing"

func TestCanonicalSpaceIDNormalizesValidAliases(t *testing.T) {
	got, err := canonicalSpaceID("49400172-EC60-4646-92C8-331D1DB78A4A")
	if err != nil {
		t.Fatal(err)
	}
	if got != "49400172-ec60-4646-92c8-331d1db78a4a" {
		t.Fatalf("canonical Space ID=%q", got)
	}
	if _, err := canonicalSpaceID("not-a-space"); err == nil {
		t.Fatal("invalid Space ID was accepted")
	}
}
