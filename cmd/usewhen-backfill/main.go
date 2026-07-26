package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jahwag/zestmem/internal/buildinfo"
	"github.com/jahwag/zestmem/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const manifestFormat = "zestmem-use-when-manifest/v1"

type manifest struct {
	Format               string  `json:"format"`
	SpaceID              string  `json:"space_id"`
	SourceSnapshotSHA256 string  `json:"source_snapshot_sha256"`
	Entries              []entry `json:"entries"`
}

type entry struct {
	MemoryID         string `json:"memory_id"`
	ExpectedRevision int    `json:"expected_revision"`
	ContentHash      string `json:"content_hash"`
	Title            string `json:"title"`
	UseWhen          string `json:"use_when"`
	EvidenceRule     string `json:"evidence_rule"`
	Reviewer         string `json:"reviewer"`
}

type readResult struct {
	ID              string `json:"id"`
	SpaceID         string `json:"space_id"`
	CurrentRevision struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		UseWhen     string `json:"use_when"`
		ContentHash string `json:"content_hash"`
	} `json:"current_revision"`
}

type mutationResult struct {
	Written  bool `json:"written"`
	Replayed bool `json:"replayed"`
}

type bearerTransport struct {
	token    string
	base     http.RoundTripper
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	now := time.Now()
	wait := time.Duration(0)
	if t.next.After(now) {
		wait = t.next.Sub(now)
		now = t.next
	}
	t.next = now.Add(t.interval)
	t.mu.Unlock()
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String())
		return
	}
	var planPath, expectedSHA, endpoint string
	var expectedCount, limit, requestsPerMinute int
	var apply bool
	flag.StringVar(&planPath, "plan", "", "reviewed manifest path")
	flag.StringVar(&expectedSHA, "plan-sha256", "", "required exact manifest SHA-256")
	flag.StringVar(&endpoint, "endpoint", "http://127.0.0.1:8080/mcp", "authenticated ZestMem MCP endpoint")
	flag.IntVar(&expectedCount, "expect-count", 0, "required exact entry count")
	flag.IntVar(&limit, "limit", 0, "maximum new revisions; zero means all")
	flag.IntVar(&requestsPerMinute, "requests-per-minute", 90, "pace all MCP HTTP requests below the Principal rate limit")
	flag.BoolVar(&apply, "apply", false, "apply after offline validation; default is dry-run")
	flag.Parse()
	if planPath == "" || expectedSHA == "" || expectedCount < 1 || limit < 0 || requestsPerMinute < 1 {
		log.Fatal("--plan, --plan-sha256, and a positive --expect-count are required")
	}
	plan, planSHA, err := loadManifest(planPath, expectedSHA, expectedCount)
	fatal(err)
	if !apply {
		fmt.Printf("dry_run=true entries=%d manifest_sha256=%s\n", len(plan.Entries), planSHA)
		return
	}
	token := os.Getenv("MEMORY_TOKEN")
	if token == "" {
		log.Fatal("MEMORY_TOKEN is required with --apply")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	transport := &bearerTransport{token: token, base: http.DefaultTransport, interval: time.Minute / time.Duration(requestsPerMinute)}
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	client := mcp.NewClient(&mcp.Implementation{Name: "use-when-backfill", Version: buildinfo.Version}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	fatal(err)
	defer session.Close()

	written, replayed := 0, 0
	for _, item := range plan.Entries {
		current, err := readMemory(ctx, session, item.MemoryID)
		if err != nil {
			fatal(fmt.Errorf("pre-read %s: %w", item.MemoryID, err))
		}
		if err := verifySource(plan.SpaceID, item, current); err != nil {
			fatal(err)
		}
		alreadyApplied := current.CurrentRevision.Number == item.ExpectedRevision+1 && current.CurrentRevision.UseWhen == item.UseWhen
		if current.CurrentRevision.Number != item.ExpectedRevision && !alreadyApplied {
			fatal(fmt.Errorf("conflict %s: current revision=%d cue_hash=%s", item.MemoryID, current.CurrentRevision.Number, cueHash(current.CurrentRevision.UseWhen)))
		}
		if !alreadyApplied && current.CurrentRevision.UseWhen != "" {
			fatal(fmt.Errorf("conflict %s: source revision already has cue hash %s", item.MemoryID, cueHash(current.CurrentRevision.UseWhen)))
		}
		if !alreadyApplied && limit > 0 && written >= limit {
			break
		}
		key := idempotencyKey(planSHA, item)
		result, err := callTool(ctx, session, "revise_memory", map[string]any{
			"memory_id": item.MemoryID, "expected_revision": item.ExpectedRevision,
			"changed_fields":  map[string]any{"use_when": item.UseWhen},
			"reason":          fmt.Sprintf("Backfill reviewed legacy use_when cue from manifest %s.", planSHA),
			"idempotency_key": key,
		})
		if err != nil {
			fatal(fmt.Errorf("revise %s: %w", item.MemoryID, err))
		}
		var mutation mutationResult
		if err := json.Unmarshal(result, &mutation); err != nil {
			fatal(fmt.Errorf("decode revise %s: %w", item.MemoryID, err))
		}
		if mutation.Written {
			written++
		} else if mutation.Replayed {
			replayed++
		} else {
			fatal(fmt.Errorf("revise %s was neither written nor replayed", item.MemoryID))
		}
		verified, err := readMemory(ctx, session, item.MemoryID)
		if err != nil {
			fatal(fmt.Errorf("post-read %s: %w", item.MemoryID, err))
		}
		if verified.CurrentRevision.Number != item.ExpectedRevision+1 || verified.CurrentRevision.UseWhen != item.UseWhen || verified.CurrentRevision.ContentHash != item.ContentHash {
			fatal(fmt.Errorf("post-verify %s failed: revision=%d cue_hash=%s content_hash=%s", item.MemoryID, verified.CurrentRevision.Number, cueHash(verified.CurrentRevision.UseWhen), verified.CurrentRevision.ContentHash))
		}
		fmt.Printf("memory_id=%s status=%s revision=%d\n", item.MemoryID, map[bool]string{true: "written", false: "replayed"}[mutation.Written], verified.CurrentRevision.Number)
	}
	fmt.Printf("complete manifest_sha256=%s written=%d replayed=%d\n", planSHA, written, replayed)
}

func loadManifest(path, expectedSHA string, expectedCount int) (manifest, string, error) {
	if !validSHA256(expectedSHA) {
		return manifest{}, "", errors.New("--plan-sha256 must be exactly 64 hexadecimal characters")
	}
	file, err := os.Open(path)
	if err != nil {
		return manifest{}, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return manifest{}, "", err
	}
	if !info.Mode().IsRegular() {
		return manifest{}, "", errors.New("manifest must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return manifest{}, "", fmt.Errorf("manifest permissions %04o expose reviewed cues; require 0600", info.Mode().Perm())
	}
	if info.Size() > 2<<20 {
		return manifest{}, "", errors.New("manifest exceeds 2 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return manifest{}, "", err
	}
	if len(raw) > 2<<20 {
		return manifest{}, "", errors.New("manifest exceeds 2 MiB")
	}
	sum := sha256.Sum256(raw)
	actualSHA := hex.EncodeToString(sum[:])
	if !strings.EqualFold(actualSHA, expectedSHA) {
		return manifest{}, "", fmt.Errorf("manifest SHA-256=%s, expected %s", actualSHA, expectedSHA)
	}
	var plan manifest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return manifest{}, "", err
	}
	if err := ensureEOF(decoder); err != nil {
		return manifest{}, "", err
	}
	if plan.Format != manifestFormat || uuid.Validate(plan.SpaceID) != nil || len(plan.Entries) != expectedCount || !validSHA256(plan.SourceSnapshotSHA256) {
		return manifest{}, "", fmt.Errorf("manifest format=%q Space=%q entries=%d", plan.Format, plan.SpaceID, len(plan.Entries))
	}
	seen := map[string]bool{}
	for i, item := range plan.Entries {
		if uuid.Validate(item.MemoryID) != nil || item.ExpectedRevision < 1 || !validSHA256(item.ContentHash) || item.Title == "" || item.EvidenceRule == "" || item.Reviewer == "" {
			return manifest{}, "", fmt.Errorf("entry %d has incomplete source identity or review evidence", i)
		}
		if seen[item.MemoryID] {
			return manifest{}, "", fmt.Errorf("entry %d duplicates memory %s", i, item.MemoryID)
		}
		seen[item.MemoryID] = true
		normalized, err := memory.NormalizeUseWhen(item.UseWhen)
		if err != nil || normalized == "" || normalized != item.UseWhen || strings.Contains(item.UseWhen, "…") || strings.Contains(item.UseWhen, "...") {
			return manifest{}, "", fmt.Errorf("entry %d cue is not exact normalized non-truncated metadata: %v", i, err)
		}
	}
	return plan, actualSHA, nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("manifest contains trailing JSON")
		}
		return err
	}
	return nil
}

func verifySource(spaceID string, item entry, current readResult) error {
	if current.ID != item.MemoryID || current.SpaceID != spaceID || current.CurrentRevision.Title != item.Title || current.CurrentRevision.ContentHash != item.ContentHash {
		return fmt.Errorf("source mismatch %s: Space/title/content identity changed", item.MemoryID)
	}
	return nil
}

func readMemory(ctx context.Context, session *mcp.ClientSession, id string) (readResult, error) {
	raw, err := callTool(ctx, session, "read_memory", map[string]any{"memory_id": id})
	if err != nil {
		return readResult{}, err
	}
	var result readResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return readResult{}, err
	}
	return result, nil
}

func callTool(ctx context.Context, session *mcp.ClientSession, name string, arguments map[string]any) ([]byte, error) {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, err
	}
	if len(result.Content) == 0 {
		return nil, errors.New("empty tool result")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return nil, errors.New("non-text tool result")
	}
	if result.IsError {
		return nil, errors.New(text.Text)
	}
	return []byte(text.Text), nil
}

func cueHash(cue string) string {
	sum := sha256.Sum256([]byte(cue))
	return hex.EncodeToString(sum[:])
}

func idempotencyKey(manifestSHA string, item entry) string {
	seed := fmt.Sprintf("zestmem-usewhen-backfill-v1:%s:%s:%d:%s", manifestSHA, item.MemoryID, item.ExpectedRevision, cueHash(item.UseWhen))
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
