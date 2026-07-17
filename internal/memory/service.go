package memory

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrForbidden        = errors.New("forbidden")
	ErrNotFound         = errors.New("memory not found")
	ErrConflict         = errors.New("revision conflict")
	ErrDuplicate        = errors.New("exact duplicate")
	ErrInvalidInput     = errors.New("invalid input")
	ErrIdempotencyReuse = errors.New("idempotency key reused with different input")
)

type Authorizer interface {
	Require(context.Context, Principal, string, Action) error
	ReadableSpaces(context.Context, Principal) ([]string, error)
}

type Repository interface {
	FindIdempotent(context.Context, string, string, string, string) (MutationResult, bool, error)
	FindExact(context.Context, string, string, string, string) (*Memory, error)
	FindSimilar(context.Context, string, string, string, []float32, int) ([]DuplicateCandidate, error)
	Create(context.Context, CreateRecord) (Memory, error)
	ReadAuthorized(context.Context, string, Principal) (Memory, error)
	Revise(context.Context, ReviseRecord) (Memory, error)
	Retract(context.Context, RetractRecord) (Memory, error)
	Recall(context.Context, RecallQuery) ([]Hit, error)
	Catalog(context.Context, string, []string, []WorkspaceClaim) ([]CatalogEntry, error)
	ResolveWorkspace(context.Context, []WorkspaceClaim) ([]string, error)
}

type CreateRecord struct {
	PrincipalID, SpaceID, Title, Body, Reason, ContentHash string
	IdempotencyKey, RequestHash, NewID                     string
	Kind                                                   Kind
	Topics                                                 []string
	Provenance                                             []Provenance
	ValidUntil                                             *time.Time
	Now                                                    time.Time
	Embedding                                              []float32
}

type ReviseRecord struct {
	PrincipalID, MemoryID, Title, Body, Reason, ContentHash string
	IdempotencyKey, RequestHash                             string
	ExpectedRevision                                        int
	Kind                                                    Kind
	Topics                                                  []string
	Provenance                                              []Provenance
	ValidUntil                                              *time.Time
	Now                                                     time.Time
	Embedding                                               []float32
}

type RetractRecord struct {
	PrincipalID, MemoryID, Reason, IdempotencyKey, RequestHash string
	ExpectedRevision                                           int
	Now                                                        time.Time
}

type RememberCommand struct {
	Principal                                    Principal
	SpaceID, Title, Body, Reason, IdempotencyKey string
	DuplicateResolution                          string
	Kind                                         Kind
	Topics                                       []string
	Provenance                                   []Provenance
	ValidUntil                                   *time.Time
	WorkspaceClaims                              []WorkspaceClaim
}

type ReviseCommand struct {
	Principal                                     Principal
	MemoryID, Title, Body, Reason, IdempotencyKey string
	ExpectedRevision                              int
	Kind                                          Kind
	Topics                                        []string
	Provenance                                    []Provenance
	ValidUntil                                    *time.Time
}

type RetractCommand struct {
	Principal                        Principal
	MemoryID, Reason, IdempotencyKey string
	ExpectedRevision                 int
}

type Service struct {
	repo     Repository
	auth     Authorizer
	embedder Embedder
	now      func() time.Time
	id       func() string
}

type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
}

func NewService(repo Repository, auth Authorizer) *Service {
	return &Service{
		repo: repo,
		auth: auth,
		now:  func() time.Time { return time.Now().UTC() },
		id:   func() string { return uuid.Must(uuid.NewV7()).String() },
	}
}

func (s *Service) WithEmbedder(embedder Embedder) *Service {
	s.embedder = embedder
	return s
}

func (s *Service) embed(ctx context.Context, text string) ([]float32, error) {
	if s.embedder == nil {
		return nil, nil
	}
	return s.embedder.Embed(ctx, text)
}

func (s *Service) Remember(ctx context.Context, cmd RememberCommand) (MutationResult, error) {
	if cmd.DuplicateResolution != "" && cmd.DuplicateResolution != "create_distinct" {
		return MutationResult{}, fmt.Errorf("%w: duplicate_resolution must be create_distinct", ErrInvalidInput)
	}
	if cmd.SpaceID == "" && len(cmd.WorkspaceClaims) > 0 {
		matches, err := s.repo.ResolveWorkspace(ctx, cmd.WorkspaceClaims)
		if err != nil {
			return MutationResult{}, err
		}
		authorized := matches[:0]
		for _, match := range matches {
			err := s.auth.Require(ctx, cmd.Principal, match, ActionCreate)
			if err == nil {
				authorized = append(authorized, match)
			} else if !errors.Is(err, ErrForbidden) {
				return MutationResult{}, err
			}
		}
		if len(authorized) != 1 {
			return MutationResult{}, fmt.Errorf("%w: workspace claims resolved to %d writable Spaces; specify target_space_id", ErrInvalidInput, len(authorized))
		}
		cmd.SpaceID = authorized[0]
	}
	if err := validateMutation(cmd.SpaceID, cmd.Title, cmd.Body, cmd.Kind, cmd.Reason, cmd.IdempotencyKey); err != nil {
		return MutationResult{}, err
	}
	if err := s.auth.Require(ctx, cmd.Principal, cmd.SpaceID, ActionCreate); err != nil {
		return MutationResult{}, err
	}
	cmd.Topics = normalizeTopics(cmd.Topics)
	if len(cmd.Provenance) == 0 {
		cmd.Provenance = []Provenance{{Class: "unverified"}}
	}
	if err := validateMetadata(cmd.Topics, cmd.Provenance); err != nil {
		return MutationResult{}, err
	}
	hash := contentHash(cmd.Title, cmd.Body, cmd.Kind)
	reqHash := requestHash("remember", cmd.SpaceID, hash, strings.TrimSpace(cmd.Reason), mutationMetadataHash(cmd.Topics, cmd.Provenance, cmd.ValidUntil))
	if result, ok, err := s.repo.FindIdempotent(ctx, cmd.Principal.ID, "remember", cmd.IdempotencyKey, reqHash); err != nil {
		return MutationResult{}, err
	} else if ok {
		return replayed(result), nil
	}
	if duplicate, err := s.repo.FindExact(ctx, cmd.Principal.ID, cmd.SpaceID, hash, ""); err != nil {
		return MutationResult{}, err
	} else if duplicate != nil {
		return MutationResult{
			Memory: *duplicate,
			DuplicateCandidates: []DuplicateCandidate{{
				MemoryID: duplicate.ID, Title: duplicate.CurrentRevision.Title,
				Excerpt: excerpt(duplicate.CurrentRevision.Body, 240), Score: 1, Exact: true,
			}},
		}, ErrDuplicate
	}
	embedding, err := s.embed(ctx, strings.TrimSpace(cmd.Title+"\n"+cmd.Body))
	if err != nil {
		return MutationResult{}, err
	}
	if cmd.DuplicateResolution != "create_distinct" {
		candidates, err := s.repo.FindSimilar(ctx, cmd.Principal.ID, cmd.SpaceID, strings.TrimSpace(cmd.Title+" "+cmd.Body), embedding, 3)
		if err != nil {
			return MutationResult{}, err
		}
		if len(candidates) > 0 {
			return MutationResult{DuplicateCandidates: candidates}, nil
		}
	}
	mem, err := s.repo.Create(ctx, CreateRecord{
		PrincipalID: cmd.Principal.ID, SpaceID: cmd.SpaceID,
		Title: strings.TrimSpace(cmd.Title), Body: strings.TrimSpace(cmd.Body),
		Kind: cmd.Kind, Topics: cmd.Topics, Provenance: cmd.Provenance,
		ValidUntil: cmd.ValidUntil, Reason: strings.TrimSpace(cmd.Reason),
		ContentHash: hash, IdempotencyKey: cmd.IdempotencyKey,
		RequestHash: reqHash, Now: s.now(), NewID: s.id(),
		Embedding: embedding,
	})
	if err != nil {
		if result, ok, retryErr := s.repo.FindIdempotent(ctx, cmd.Principal.ID, "remember", cmd.IdempotencyKey, reqHash); retryErr == nil && ok {
			return replayed(result), nil
		}
		return MutationResult{}, err
	}
	return MutationResult{Memory: mem, Written: true}, nil
}

func (s *Service) Revise(ctx context.Context, cmd ReviseCommand) (MutationResult, error) {
	if cmd.MemoryID == "" || cmd.ExpectedRevision < 1 {
		return MutationResult{}, fmt.Errorf("%w: memory_id and expected_revision are required", ErrInvalidInput)
	}
	if err := validateMutation("existing", cmd.Title, cmd.Body, cmd.Kind, cmd.Reason, cmd.IdempotencyKey); err != nil {
		return MutationResult{}, err
	}
	cmd.Topics = normalizeTopics(cmd.Topics)
	if len(cmd.Provenance) == 0 {
		cmd.Provenance = []Provenance{{Class: "unverified"}}
	}
	if err := validateMetadata(cmd.Topics, cmd.Provenance); err != nil {
		return MutationResult{}, err
	}
	hash := contentHash(cmd.Title, cmd.Body, cmd.Kind)
	reqHash := requestHash("revise", cmd.MemoryID, fmt.Sprint(cmd.ExpectedRevision), hash, strings.TrimSpace(cmd.Reason), mutationMetadataHash(cmd.Topics, cmd.Provenance, cmd.ValidUntil))
	current, err := s.repo.ReadAuthorized(ctx, cmd.MemoryID, cmd.Principal)
	if err != nil {
		return MutationResult{}, err
	}
	if err := s.auth.Require(ctx, cmd.Principal, current.SpaceID, ActionRevise); err != nil {
		return MutationResult{}, err
	}
	if result, ok, err := s.repo.FindIdempotent(ctx, cmd.Principal.ID, "revise", cmd.IdempotencyKey, reqHash); err != nil {
		return MutationResult{}, err
	} else if ok {
		return replayed(result), nil
	}
	if current.CurrentRevision.Number != cmd.ExpectedRevision {
		return MutationResult{}, ErrConflict
	}
	embedding, err := s.embed(ctx, strings.TrimSpace(cmd.Title+"\n"+cmd.Body))
	if err != nil {
		return MutationResult{}, err
	}
	if duplicate, err := s.repo.FindExact(ctx, cmd.Principal.ID, current.SpaceID, hash, cmd.MemoryID); err != nil {
		return MutationResult{}, err
	} else if duplicate != nil {
		return MutationResult{}, ErrDuplicate
	}
	mem, err := s.repo.Revise(ctx, ReviseRecord{
		PrincipalID: cmd.Principal.ID, MemoryID: cmd.MemoryID,
		ExpectedRevision: cmd.ExpectedRevision, Title: strings.TrimSpace(cmd.Title),
		Body: strings.TrimSpace(cmd.Body), Kind: cmd.Kind, Topics: cmd.Topics,
		Provenance: cmd.Provenance, ValidUntil: cmd.ValidUntil,
		Reason: strings.TrimSpace(cmd.Reason), ContentHash: hash,
		IdempotencyKey: cmd.IdempotencyKey, RequestHash: reqHash, Now: s.now(),
		Embedding: embedding,
	})
	if err != nil {
		if result, ok, retryErr := s.repo.FindIdempotent(ctx, cmd.Principal.ID, "revise", cmd.IdempotencyKey, reqHash); retryErr == nil && ok {
			return replayed(result), nil
		}
		return MutationResult{}, err
	}
	return MutationResult{Memory: mem, Written: true}, nil
}

func (s *Service) Retract(ctx context.Context, cmd RetractCommand) (MutationResult, error) {
	if cmd.MemoryID == "" || cmd.ExpectedRevision < 1 || strings.TrimSpace(cmd.Reason) == "" || cmd.IdempotencyKey == "" {
		return MutationResult{}, fmt.Errorf("%w: memory_id, expected_revision, reason, and idempotency_key are required", ErrInvalidInput)
	}
	current, err := s.repo.ReadAuthorized(ctx, cmd.MemoryID, cmd.Principal)
	if err != nil {
		return MutationResult{}, err
	}
	if err := s.auth.Require(ctx, cmd.Principal, current.SpaceID, ActionRetract); err != nil {
		return MutationResult{}, err
	}
	reqHash := requestHash("retract", cmd.MemoryID, fmt.Sprint(cmd.ExpectedRevision), cmd.Reason)
	if result, ok, err := s.repo.FindIdempotent(ctx, cmd.Principal.ID, "retract", cmd.IdempotencyKey, reqHash); err != nil {
		return MutationResult{}, err
	} else if ok {
		return replayed(result), nil
	}
	if current.CurrentRevision.Number != cmd.ExpectedRevision {
		return MutationResult{}, ErrConflict
	}
	mem, err := s.repo.Retract(ctx, RetractRecord{
		PrincipalID: cmd.Principal.ID, MemoryID: cmd.MemoryID,
		ExpectedRevision: cmd.ExpectedRevision, Reason: strings.TrimSpace(cmd.Reason),
		IdempotencyKey: cmd.IdempotencyKey, RequestHash: reqHash, Now: s.now(),
	})
	if err != nil {
		if result, ok, retryErr := s.repo.FindIdempotent(ctx, cmd.Principal.ID, "retract", cmd.IdempotencyKey, reqHash); retryErr == nil && ok {
			return replayed(result), nil
		}
		return MutationResult{}, err
	}
	return MutationResult{Memory: mem, Written: true}, nil
}

func replayed(result MutationResult) MutationResult {
	result.Written = false
	result.Replayed = true
	return result
}

func (s *Service) Read(ctx context.Context, principal Principal, memoryID string) (Memory, error) {
	return s.repo.ReadAuthorized(ctx, memoryID, principal)
}

func (s *Service) Catalog(ctx context.Context, principal Principal, claims []WorkspaceClaim, cursor ...string) (CatalogResult, error) {
	if len(claims) > 10 {
		return CatalogResult{}, fmt.Errorf("%w: at most 10 workspace claims", ErrInvalidInput)
	}
	for _, claim := range claims {
		if len(claim.Value) > 4096 {
			return CatalogResult{}, fmt.Errorf("%w: workspace claim too long", ErrInvalidInput)
		}
	}
	spaces, err := s.auth.ReadableSpaces(ctx, principal)
	if err != nil {
		return CatalogResult{}, err
	}
	entries, err := s.repo.Catalog(ctx, principal.ID, spaces, claims)
	if err != nil {
		return CatalogResult{}, err
	}
	offset := 0
	cursorKind := "catalog-" + requestHash(fmt.Sprint(claims))[:12]
	if len(cursor) > 0 && cursor[0] != "" {
		offset, err = decodeCursor(cursorKind, cursor[0])
		if err != nil {
			return CatalogResult{}, err
		}
	}
	if offset > len(entries) {
		return CatalogResult{}, fmt.Errorf("%w: catalog cursor is out of range", ErrInvalidInput)
	}
	entries = entries[offset:]
	const tokenBudget = 800
	if len(entries) > 0 && !withinTokenBudget(CatalogResult{Spaces: entries[:1]}, tokenBudget) {
		entries = append([]CatalogEntry(nil), entries...)
		entries[0].Topics = nil
		entries[0].Name = shorten(entries[0].Name, 128)
		entries[0].Slug = shorten(entries[0].Slug, 64)
	}
	keep := 0
	for i := range entries {
		candidate := CatalogResult{Spaces: entries[:i+1], OmittedCount: len(entries) - (i + 1)}
		if candidate.OmittedCount > 0 {
			candidate.NextCursor = encodeCursor(cursorKind, offset+i+1)
		}
		if !withinTokenBudget(candidate, tokenBudget) {
			break
		}
		keep++
	}
	result := CatalogResult{Spaces: entries[:keep], OmittedCount: len(entries) - keep}
	if result.OmittedCount > 0 {
		result.NextCursor = encodeCursor(cursorKind, offset+keep)
	}
	return result, nil
}

func (s *Service) Recall(ctx context.Context, query RecallQuery) ([]Hit, error) {
	result, err := s.RecallPage(ctx, query)
	return result.Hits, err
}

func (s *Service) RecallPage(ctx context.Context, query RecallQuery) (RecallResult, error) {
	query.Query = strings.TrimSpace(query.Query)
	query.Topics = normalizeTopics(query.Topics)
	if query.Query == "" {
		return RecallResult{}, fmt.Errorf("%w: query is required", ErrInvalidInput)
	}
	if len(query.Query) > 4096 || len(query.SpaceIDs) > 100 || len(query.Topics) > 32 || len(query.Kinds) > 6 || len(query.WorkspaceClaims) > 10 {
		return RecallResult{}, fmt.Errorf("%w: recall filters exceed limits", ErrInvalidInput)
	}
	if query.Limit == 0 {
		query.Limit = 6
	}
	if query.Limit < 1 || query.Limit > 20 {
		return RecallResult{}, fmt.Errorf("%w: limit must be between 1 and 20", ErrInvalidInput)
	}
	requested := query.Limit
	cursorKind := "recall-" + requestHash(query.Query, fmt.Sprint(query.SpaceIDs), fmt.Sprint(query.WorkspaceClaims), fmt.Sprint(query.Kinds), fmt.Sprint(query.Topics), fmt.Sprint(requested))[:12]
	var err error
	if query.Cursor != "" {
		query.Offset, err = decodeCursor(cursorKind, query.Cursor)
		if err != nil {
			return RecallResult{}, err
		}
	}
	query.Limit = requested + 1
	readable, err := s.auth.ReadableSpaces(ctx, query.Principal)
	if err != nil {
		return RecallResult{}, err
	}
	if len(query.SpaceIDs) == 0 && len(query.WorkspaceClaims) > 0 {
		query.SpaceIDs, err = s.repo.ResolveWorkspace(ctx, query.WorkspaceClaims)
		if err != nil {
			return RecallResult{}, err
		}
	}
	if len(query.SpaceIDs) > 0 {
		query.SpaceIDs = intersectSpaces(query.SpaceIDs, readable)
	} else {
		query.SpaceIDs = readable
	}
	query.Embedding, err = s.embed(ctx, query.Query)
	if err != nil {
		return RecallResult{}, err
	}
	hits, err := s.repo.Recall(ctx, query)
	if err != nil {
		return RecallResult{}, err
	}
	more := len(hits) > requested
	if more {
		hits = hits[:requested]
	}
	const tokenBudget = 2000
	for i := range hits {
		if !withinTokenBudget(RecallResult{Hits: hits[i : i+1]}, tokenBudget) {
			hits[i].Topics = nil
			hits[i].Excerpt = shorten(hits[i].Excerpt, 200)
			hits[i].Title = shorten(hits[i].Title, 256)
		}
		if !withinTokenBudget(RecallResult{Hits: hits[i : i+1]}, tokenBudget) {
			hits[i].Excerpt = shorten(hits[i].Excerpt, 64)
			hits[i].Title = shorten(hits[i].Title, 64)
		}
	}
	keep := 0
	for i := range hits {
		if !withinTokenBudget(RecallResult{Hits: hits[:i+1]}, tokenBudget) {
			break
		}
		keep++
	}
	if keep < len(hits) {
		more = true
	}
	result := RecallResult{Hits: hits[:keep]}
	if more {
		result.NextCursor = encodeCursor(cursorKind, query.Offset+keep)
	}
	return result, nil
}

func encodeCursor(kind string, offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + strconv.Itoa(offset)))
}
func decodeCursor(kind, cursor string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid cursor", ErrInvalidInput)
	}
	prefix := kind + ":"
	if !strings.HasPrefix(string(raw), prefix) {
		return 0, fmt.Errorf("%w: cursor has wrong type", ErrInvalidInput)
	}
	offset, err := strconv.Atoi(strings.TrimPrefix(string(raw), prefix))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("%w: invalid cursor", ErrInvalidInput)
	}
	return offset, nil
}

func withinTokenBudget(value any, budget int) bool {
	raw, err := json.Marshal(value)
	return err == nil && conservativeTokenCount(raw) <= budget
}

// The serialized MCP content is JSON/UTF-8. Counting every byte as a token is
// a true worst-case bound for the byte-backed tokenizers used by the supported
// clients: every token consumes at least one input byte. Real counts are lower.
func conservativeTokenCount(raw []byte) int {
	return len(raw)
}

func shorten(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func validateMutation(spaceID, title, body string, kind Kind, reason, key string) error {
	switch {
	case strings.TrimSpace(spaceID) == "":
		return fmt.Errorf("%w: target Space is required", ErrInvalidInput)
	case strings.TrimSpace(title) == "":
		return fmt.Errorf("%w: title is required", ErrInvalidInput)
	case len([]byte(strings.TrimSpace(title))) > 512:
		return fmt.Errorf("%w: title exceeds 512 bytes", ErrInvalidInput)
	case strings.TrimSpace(body) == "":
		return fmt.Errorf("%w: body is required", ErrInvalidInput)
	case len([]byte(strings.TrimSpace(body))) > MaxBodyBytes:
		return fmt.Errorf("%w: body exceeds %d bytes", ErrInvalidInput, MaxBodyBytes)
	case !kind.Valid():
		return fmt.Errorf("%w: unsupported kind %q", ErrInvalidInput, kind)
	case strings.TrimSpace(reason) == "":
		return fmt.Errorf("%w: reason is required", ErrInvalidInput)
	case len([]byte(strings.TrimSpace(reason))) > 1000:
		return fmt.Errorf("%w: reason exceeds 1000 bytes", ErrInvalidInput)
	case strings.TrimSpace(key) == "":
		return fmt.Errorf("%w: idempotency_key is required", ErrInvalidInput)
	case len(key) > 200:
		return fmt.Errorf("%w: idempotency_key exceeds 200 bytes", ErrInvalidInput)
	default:
		return nil
	}
}

func validateMetadata(topics []string, provenance []Provenance) error {
	if len(topics) > 32 {
		return fmt.Errorf("%w: at most 32 topics", ErrInvalidInput)
	}
	for _, topic := range topics {
		if len(topic) > 80 {
			return fmt.Errorf("%w: topic exceeds 80 bytes", ErrInvalidInput)
		}
	}
	if len(provenance) > 16 {
		return fmt.Errorf("%w: at most 16 provenance entries", ErrInvalidInput)
	}
	for _, item := range provenance {
		if len(item.Class) > 80 || len(item.Locator) > 2048 {
			return fmt.Errorf("%w: provenance entry too long", ErrInvalidInput)
		}
	}
	return nil
}

func normalizeTopics(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, topic := range in {
		topic = strings.ToLower(strings.Join(strings.Fields(topic), " "))
		if topic != "" && !seen[topic] {
			seen[topic] = true
			out = append(out, topic)
		}
	}
	return out
}

func contentHash(title, body string, kind Kind) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(title)) + "\x00" + strings.TrimSpace(body) + "\x00" + string(kind)))
	return hex.EncodeToString(sum[:])
}

func requestHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func mutationMetadataHash(topics []string, provenance []Provenance, validUntil *time.Time) string {
	raw, _ := json.Marshal(struct {
		Topics     []string     `json:"topics"`
		Provenance []Provenance `json:"provenance"`
		ValidUntil *time.Time   `json:"valid_until"`
	}{topics, provenance, validUntil})
	return requestHash(string(raw))
}

func excerpt(body string, limit int) string {
	body = strings.Join(strings.Fields(body), " ")
	if len(body) <= limit {
		if len(body) <= 1 {
			return "…"
		}
		return body[:len(body)-1] + "…"
	}
	return body[:limit-1] + "…"
}

func intersectSpaces(requested, readable []string) []string {
	allowed := make(map[string]bool, len(readable))
	for _, id := range readable {
		allowed[id] = true
	}
	out := make([]string, 0, len(requested))
	for _, id := range requested {
		if allowed[id] {
			out = append(out, id)
		}
	}
	return out
}
