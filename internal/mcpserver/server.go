package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bytelope/zestmem/internal/authn"
	"github.com/bytelope/zestmem/internal/embed"
	"github.com/bytelope/zestmem/internal/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	domain   *memory.Service
	mcp      *mcp.Server
	observer Observer
}

type Observer interface {
	Observe(string, time.Duration, error)
}

func New(domain *memory.Service, observers ...Observer) *Server {
	s := &Server{domain: domain}
	if len(observers) > 0 {
		s.observer = observers[0]
	}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "agent-memory", Version: "0.1.0"}, nil)
	s.register()
	return s
}

func (s *Server) observed(operation string, start time.Time, err error) {
	if s.observer != nil {
		s.observer.Observe(operation, time.Since(start), err)
	}
}

func (s *Server) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)
}

type catalogInput struct {
	WorkspaceClaims []memory.WorkspaceClaim `json:"workspace_claims,omitempty" jsonschema:"Optional registered git_remote or local_path hints"`
	Cursor          string                  `json:"cursor,omitempty" jsonschema:"Reserved pagination cursor"`
}

type recallInput struct {
	Query           string                  `json:"query" jsonschema:"Task-specific search query"`
	WorkspaceClaims []memory.WorkspaceClaim `json:"workspace_claims,omitempty"`
	SpaceIDs        []string                `json:"space_ids,omitempty"`
	Kinds           []memory.Kind           `json:"kinds,omitempty"`
	Topics          []string                `json:"topics,omitempty"`
	Limit           int                     `json:"limit,omitempty" jsonschema:"Result count, default 6 and maximum 20"`
	Cursor          string                  `json:"cursor,omitempty"`
}

type readInput struct {
	MemoryID string `json:"memory_id" jsonschema:"Memory UUID returned by recall"`
}

type rememberInput struct {
	TargetSpaceID       string                  `json:"target_space_id" jsonschema:"Target Space UUID returned by catalog"`
	WorkspaceClaims     []memory.WorkspaceClaim `json:"workspace_claims,omitempty"`
	Title               string                  `json:"title"`
	Body                string                  `json:"body"`
	Kind                memory.Kind             `json:"kind"`
	Topics              []string                `json:"topics,omitempty"`
	Provenance          []memory.Provenance     `json:"provenance,omitempty"`
	ValidUntil          *time.Time              `json:"valid_until,omitempty"`
	Reason              string                  `json:"reason" jsonschema:"Human-readable reason for making durable knowledge"`
	IdempotencyKey      string                  `json:"idempotency_key"`
	DuplicateResolution string                  `json:"duplicate_resolution,omitempty" jsonschema:"Set to create_distinct only after reviewing candidates"`
}

type reviseFields struct {
	Title       *string              `json:"title,omitempty"`
	Body        *string              `json:"body,omitempty"`
	Kind        *memory.Kind         `json:"kind,omitempty"`
	Topics      *[]string            `json:"topics,omitempty"`
	Provenance  *[]memory.Provenance `json:"provenance,omitempty"`
	ValidUntil  *time.Time           `json:"valid_until,omitempty"`
	ClearExpiry bool                 `json:"clear_valid_until,omitempty"`
}

func (f reviseFields) changed() bool {
	return f.Title != nil || f.Body != nil || f.Kind != nil || f.Topics != nil || f.Provenance != nil || f.ValidUntil != nil || f.ClearExpiry
}

type reviseInput struct {
	MemoryID         string       `json:"memory_id"`
	ExpectedRevision int          `json:"expected_revision"`
	ChangedFields    reviseFields `json:"changed_fields"`
	Reason           string       `json:"reason"`
	IdempotencyKey   string       `json:"idempotency_key"`
}

type retractInput struct {
	MemoryID         string `json:"memory_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
}

func (s *Server) register() {
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "catalog", Description: "Return a bounded map of accessible Spaces and Topics; call before task work and after context loss.", Annotations: readOnlyAnnotations()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in catalogInput) (*mcp.CallToolResult, any, error) {
			start := time.Now()
			principal, err := principal(ctx)
			if err != nil {
				return toolError(err)
			}
			out, err := s.domain.Catalog(ctx, principal, in.WorkspaceClaims, in.Cursor)
			s.observed("catalog", start, err)
			return toolResult(out, err)
		})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "recall", Description: "Search durable memory with explicit task-specific terms; returns compact hits without bodies.", Annotations: readOnlyAnnotations()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in recallInput) (*mcp.CallToolResult, any, error) {
			start := time.Now()
			principal, err := principal(ctx)
			if err != nil {
				return toolError(err)
			}
			out, err := s.domain.RecallPage(ctx, memory.RecallQuery{Query: in.Query, WorkspaceClaims: in.WorkspaceClaims, SpaceIDs: in.SpaceIDs, Kinds: in.Kinds, Topics: in.Topics, Limit: in.Limit, Principal: principal, Cursor: in.Cursor})
			s.observed("recall", start, err)
			return toolResult(out, err)
		})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "read_memory", Description: "Read one full Memory body by ID after selecting it from recall.", Annotations: readOnlyAnnotations()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, any, error) {
			start := time.Now()
			principal, err := principal(ctx)
			if err != nil {
				return toolError(err)
			}
			out, err := s.domain.Read(ctx, principal, in.MemoryID)
			s.observed("read_memory", start, err)
			return toolResult(out, err)
		})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "remember", Description: "Explicitly write durable knowledge to the required target_space_id; checks duplicates first and never captures transcripts.", Annotations: mutationAnnotations(false)},
		func(ctx context.Context, _ *mcp.CallToolRequest, in rememberInput) (*mcp.CallToolResult, any, error) {
			start := time.Now()
			principal, err := principal(ctx)
			if err != nil {
				return toolError(err)
			}
			out, err := s.domain.Remember(ctx, memory.RememberCommand{Principal: principal, SpaceID: in.TargetSpaceID, WorkspaceClaims: in.WorkspaceClaims, Title: in.Title, Body: in.Body, Kind: in.Kind, Topics: in.Topics, Provenance: in.Provenance, ValidUntil: in.ValidUntil, Reason: in.Reason, IdempotencyKey: in.IdempotencyKey, DuplicateResolution: in.DuplicateResolution})
			s.observed("remember", start, err)
			if errors.Is(err, memory.ErrDuplicate) {
				return toolErrorValue(compactMutation(out), err)
			}
			return toolResult(compactMutation(out), err)
		})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "revise_memory", Description: "Create an immutable next revision using optimistic concurrency.", Annotations: mutationAnnotations(false)},
		func(ctx context.Context, _ *mcp.CallToolRequest, in reviseInput) (*mcp.CallToolResult, any, error) {
			start := time.Now()
			if !in.ChangedFields.changed() {
				err := fmt.Errorf("%w: changed_fields must contain at least one change", memory.ErrInvalidInput)
				s.observed("revise_memory", start, err)
				return toolError(err)
			}
			p, err := principal(ctx)
			if err != nil {
				return toolError(err)
			}
			current, err := s.domain.Read(ctx, p, in.MemoryID)
			if err != nil {
				return toolResult(nil, err)
			}
			r := current.CurrentRevision
			if in.ChangedFields.Title != nil {
				r.Title = *in.ChangedFields.Title
			}
			if in.ChangedFields.Body != nil {
				r.Body = *in.ChangedFields.Body
			}
			if in.ChangedFields.Kind != nil {
				r.Kind = *in.ChangedFields.Kind
			}
			if in.ChangedFields.Topics != nil {
				r.Topics = *in.ChangedFields.Topics
			}
			if in.ChangedFields.Provenance != nil {
				r.Provenance = *in.ChangedFields.Provenance
			}
			if in.ChangedFields.ClearExpiry {
				r.ValidUntil = nil
			} else if in.ChangedFields.ValidUntil != nil {
				r.ValidUntil = in.ChangedFields.ValidUntil
			}
			out, err := s.domain.Revise(ctx, memory.ReviseCommand{Principal: p, MemoryID: in.MemoryID, ExpectedRevision: in.ExpectedRevision, Title: r.Title, Body: r.Body, Kind: r.Kind, Topics: r.Topics, Provenance: r.Provenance, ValidUntil: r.ValidUntil, Reason: in.Reason, IdempotencyKey: in.IdempotencyKey})
			s.observed("revise_memory", start, err)
			return toolResult(compactMutation(out), err)
		})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "retract_memory", Description: "Soft-retract incorrect or obsolete knowledge using optimistic concurrency.", Annotations: mutationAnnotations(true)},
		func(ctx context.Context, _ *mcp.CallToolRequest, in retractInput) (*mcp.CallToolResult, any, error) {
			start := time.Now()
			p, err := principal(ctx)
			if err != nil {
				return toolError(err)
			}
			out, err := s.domain.Retract(ctx, memory.RetractCommand{Principal: p, MemoryID: in.MemoryID, ExpectedRevision: in.ExpectedRevision, Reason: in.Reason, IdempotencyKey: in.IdempotencyKey})
			s.observed("retract_memory", start, err)
			return toolResult(compactMutation(out), err)
		})
}

func readOnlyAnnotations() *mcp.ToolAnnotations {
	closed := false
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed}
}
func mutationAnnotations(destructive bool) *mcp.ToolAnnotations {
	closed := false
	return &mcp.ToolAnnotations{DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &closed}
}

func compactMutation(result memory.MutationResult) any {
	type compactMemory struct {
		ID       string        `json:"id,omitempty"`
		SpaceID  string        `json:"space_id,omitempty"`
		Status   memory.Status `json:"status,omitempty"`
		Revision int           `json:"revision,omitempty"`
		Title    string        `json:"title,omitempty"`
	}
	mem := result.Memory
	return struct {
		Memory              compactMemory               `json:"memory"`
		Written             bool                        `json:"written"`
		Replayed            bool                        `json:"replayed"`
		DuplicateCandidates []memory.DuplicateCandidate `json:"duplicate_candidates,omitempty"`
	}{
		Memory: compactMemory{ID: mem.ID, SpaceID: mem.SpaceID, Status: mem.Status, Revision: mem.CurrentRevision.Number, Title: mem.CurrentRevision.Title}, Written: result.Written, Replayed: result.Replayed, DuplicateCandidates: result.DuplicateCandidates,
	}
}

func principal(ctx context.Context) (memory.Principal, error) {
	p, ok := authn.PrincipalFromContext(ctx)
	if !ok {
		return memory.Principal{}, authn.ErrUnauthenticated
	}
	return p, nil
}

func toolResult(value any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return toolError(err)
	}
	raw, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return nil, nil, marshalErr
	}
	// Emit one compact representation. Duplicating the same payload in both
	// content and structuredContent would spend the context budget twice.
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil, nil
}

func toolError(err error) (*mcp.CallToolResult, any, error) {
	message := "operation failed"
	switch {
	case errors.Is(err, memory.ErrInvalidInput), errors.Is(err, memory.ErrConflict), errors.Is(err, memory.ErrDuplicate), errors.Is(err, memory.ErrIdempotencyReuse), errors.Is(err, memory.ErrNotFound), errors.Is(err, memory.ErrForbidden):
		message = err.Error()
	case errors.Is(err, authn.ErrUnauthenticated):
		message = "unauthenticated"
	case errors.Is(err, embed.ErrBusy):
		message = "embedding queue full; retry after 1 second"
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(`{"error":%q}`, message)}}}, nil, nil
}

func toolErrorValue(value any, err error) (*mcp.CallToolResult, any, error) {
	raw, marshalErr := json.Marshal(map[string]any{"error": err.Error(), "result": value})
	if marshalErr != nil {
		return nil, nil, marshalErr
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil, nil
}
