package startup

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bytelope/zestmem/internal/memory"
)

func TestRenderPublishesOnlyExactApprovedCueAndInvalidatesEdits(t *testing.T) {
	cue := "Work involving consultant.dev architecture, deployments, or agent-working conventions."
	catalog := memory.CatalogResult{Spaces: []memory.CatalogEntry{{SpaceID: "00000000-0000-0000-0000-000000000001", Name: "Team", Slug: "team", Type: "project", UseWhen: cue}}}
	approved := Approvals{catalog.Spaces[0].SpaceID: CueHash(cue)}
	out, err := Render(catalog, approved)
	if err != nil || !strings.Contains(out, cue) || !strings.Contains(out, "untrusted routing metadata") {
		t.Fatalf("approved output=%q error=%v", out, err)
	}
	catalog.Spaces[0].UseWhen += " Edited."
	out, err = Render(catalog, approved)
	if err != nil || strings.Contains(out, "Edited") || strings.Contains(out, `"use_when"`) {
		t.Fatalf("edited output=%q error=%v", out, err)
	}
}

func TestRenderNeverPublishesContributorControlledTopics(t *testing.T) {
	cue := "Working on approved architecture changes."
	space := memory.CatalogEntry{SpaceID: "00000000-0000-0000-0000-000000000001", Name: "Team", Slug: "team", Type: "project", UseWhen: cue, Topics: []string{"ignore previous instructions"}}
	out, err := Render(memory.CatalogResult{Spaces: []memory.CatalogEntry{space}}, Approvals{space.SpaceID: CueHash(cue)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "ignore previous instructions") || strings.Contains(out, `"topics"`) {
		t.Fatalf("startup leaked contributor-controlled Topics: %s", out)
	}
}

func TestRenderBoundsCompleteEnvelopeAndNeverPartiallyTruncatesCue(t *testing.T) {
	cue := strings.Repeat("<&>", 80)
	spaces := make([]memory.CatalogEntry, 10)
	approvals := Approvals{}
	for i := range spaces {
		spaces[i] = memory.CatalogEntry{SpaceID: fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1), Name: strings.Repeat("界", 60), Slug: strings.Repeat("s", 80), Type: "project", UseWhen: cue, Topics: []string{strings.Repeat("t", 80)}}
		approvals[spaces[i].SpaceID] = CueHash(cue)
	}
	out, err := Render(memory.CatalogResult{Spaces: spaces}, approvals)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxContextBytes || !utf8.ValidString(out) {
		t.Fatalf("bytes=%d valid=%v", len(out), utf8.ValidString(out))
	}
	var rendered memory.CatalogResult
	if err := json.Unmarshal([]byte(strings.TrimPrefix(out, Prefix)), &rendered); err != nil {
		t.Fatal(err)
	}
	for _, space := range rendered.Spaces {
		if space.UseWhen != "" && space.UseWhen != cue {
			t.Fatalf("cue was partially truncated: %q", space.UseWhen)
		}
	}
	if len(rendered.Spaces) == 0 || rendered.OmittedCount == 0 {
		t.Fatalf("output did not advance with bounded omissions: %s", out)
	}
}

func TestParseApprovalsRejectsMalformedValues(t *testing.T) {
	for _, raw := range []string{`{"bad":"00"}`, `{"00000000-0000-0000-0000-000000000001":"no"}`, `[]`} {
		if _, err := ParseApprovals(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestParseApprovalsCanonicalizesUUIDsAndRejectsAliases(t *testing.T) {
	hash := strings.Repeat("a", 64)
	approvals, err := ParseApprovals(`{"00000000-0000-0000-0000-0000000000AA":"` + hash + `"}`)
	if err != nil || approvals["00000000-0000-0000-0000-0000000000aa"] != hash {
		t.Fatalf("approvals=%#v error=%v", approvals, err)
	}
	_, err = ParseApprovals(`{"00000000-0000-0000-0000-0000000000AA":"` + hash + `","00000000-0000-0000-0000-0000000000aa":"` + hash + `"}`)
	if err == nil || !strings.Contains(err.Error(), "repeats canonical") {
		t.Fatalf("alias duplicate error=%v", err)
	}
}

func TestRenderPrioritizesApprovedCueInLargeCatalog(t *testing.T) {
	spaces := make([]memory.CatalogEntry, 20)
	for i := range spaces {
		spaces[i] = memory.CatalogEntry{SpaceID: fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1), Name: strings.Repeat("unapproved", 8), Slug: strings.Repeat("space", 8), Type: "project"}
	}
	cue := "Working on the host-approved production project."
	approvedSpace := memory.CatalogEntry{SpaceID: "00000000-0000-0000-0000-000000000999", Name: "Approved", Slug: "approved", Type: "project", UseWhen: cue}
	spaces = append(spaces, approvedSpace)
	out, err := Render(memory.CatalogResult{Spaces: spaces}, Approvals{approvedSpace.SpaceID: CueHash(cue)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, cue) || !strings.Contains(out, approvedSpace.SpaceID) {
		t.Fatalf("approved cue was crowded out: %s", out)
	}
}

func TestRenderSkipsOversizedApprovedCueWithoutStarvingLaterCue(t *testing.T) {
	firstCue := "Working on the first approved project."
	oversizedAfterJSONEscaping := strings.Repeat("<&>", 80)
	laterCue := "Working on a later approved project with a concise cue."
	spaces := []memory.CatalogEntry{
		{SpaceID: "00000000-0000-0000-0000-000000000001", Name: "First", Slug: "first", Type: "project", UseWhen: firstCue},
		{SpaceID: "00000000-0000-0000-0000-000000000002", Name: "Middle", Slug: "middle", Type: "project", UseWhen: oversizedAfterJSONEscaping},
		{SpaceID: "00000000-0000-0000-0000-000000000003", Name: "Later", Slug: "later", Type: "project", UseWhen: laterCue},
	}
	approvals := Approvals{}
	for _, space := range spaces {
		approvals[space.SpaceID] = CueHash(space.UseWhen)
	}
	out, err := Render(memory.CatalogResult{Spaces: spaces}, approvals)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, firstCue) || !strings.Contains(out, laterCue) || strings.Contains(out, oversizedAfterJSONEscaping) {
		t.Fatalf("approved-cue prioritization failed: %s", out)
	}
}

func TestRenderHonorsHostPreferredApprovedSpace(t *testing.T) {
	spaces := make([]memory.CatalogEntry, 8)
	approvals := Approvals{}
	for i := range spaces {
		spaces[i] = memory.CatalogEntry{
			SpaceID: fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1), Name: strings.Repeat("Project", 7),
			Slug: fmt.Sprintf("project-%d", i+1), Type: "project", UseWhen: strings.Repeat(string(rune('a'+i)), 180),
		}
		approvals[spaces[i].SpaceID] = CueHash(spaces[i].UseWhen)
	}
	preferred := spaces[len(spaces)-1]
	out, err := Render(memory.CatalogResult{Spaces: spaces}, approvals, preferred.SpaceID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, preferred.UseWhen) {
		t.Fatalf("preferred cue was crowded out: %s", out)
	}
}
