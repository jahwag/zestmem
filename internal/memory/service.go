package memory

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrForbidden        = errors.New("forbidden")
	ErrNotFound         = errors.New("memory not found")
	ErrConflict         = errors.New("revision conflict")
	ErrDuplicate        = errors.New("exact duplicate")
	ErrUnfamiliarTopics = errors.New("unfamiliar topics")
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
	Browse(context.Context, BrowseQuery) (RepositoryBrowseResult, error)
	Recall(context.Context, RecallQuery) (RepositoryRecallResult, error)
	Catalog(context.Context, string, []string, []WorkspaceClaim) ([]CatalogEntry, error)
	DiscoverMetadata(context.Context, MetadataQuery) (RepositoryMetadataResult, error)
	TopicUsage(context.Context, string, string, []string) (map[string]int, error)
	ResolveWorkspace(context.Context, []WorkspaceClaim) ([]string, error)
}

type CreateRecord struct {
	PrincipalID, SpaceID, Title, Body, UseWhen, Reason, ContentHash string
	IdempotencyKey, RequestHash, NewID                              string
	Kind                                                            Kind
	Topics                                                          []string
	Provenance                                                      []Provenance
	ValidUntil                                                      *time.Time
	Now                                                             time.Time
	Embedding                                                       []float32
}

type ReviseRecord struct {
	PrincipalID, MemoryID, Title, Body, UseWhen, Reason, ContentHash string
	IdempotencyKey, RequestHash                                      string
	ExpectedRevision                                                 int
	Kind                                                             Kind
	Topics                                                           []string
	Provenance                                                       []Provenance
	ValidUntil                                                       *time.Time
	Now                                                              time.Time
	Embedding                                                        []float32
	ReuseEmbeddingRevision                                           int
	PreserveContentUpdatedAt                                         bool
}

type RetractRecord struct {
	PrincipalID, MemoryID, Reason, IdempotencyKey, RequestHash string
	ExpectedRevision                                           int
	Now                                                        time.Time
}

type RememberCommand struct {
	Principal                                             Principal
	SpaceID, Title, Body, UseWhen, Reason, IdempotencyKey string
	DuplicateResolution                                   string
	ConfirmNewTopics                                      []string
	Kind                                                  Kind
	Topics                                                []string
	Provenance                                            []Provenance
	ValidUntil                                            *time.Time
	WorkspaceClaims                                       []WorkspaceClaim
}

type ReviseCommand struct {
	Principal                                              Principal
	MemoryID, Title, Body, UseWhen, Reason, IdempotencyKey string
	ExpectedRevision                                       int
	UseWhenSet                                             bool
	ConfirmNewTopics                                       []string
	Kind                                                   Kind
	Topics                                                 []string
	Provenance                                             []Provenance
	ValidUntil                                             *time.Time
}

type RetractCommand struct {
	Principal                        Principal
	MemoryID, Reason, IdempotencyKey string
	ExpectedRevision                 int
}

type Service struct {
	repo       Repository
	auth       Authorizer
	embedder   Embedder
	retrievals RetrievalRecorder
	now        func() time.Time
	id         func() string
}

type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
}

type RetrievalRecorder interface {
	RecordRetrieval(context.Context, RetrievalEvent) error
}

func NewService(repo Repository, auth Authorizer) *Service {
	service := &Service{
		repo: repo,
		auth: auth,
		now:  func() time.Time { return time.Now().UTC() },
		id:   func() string { return uuid.Must(uuid.NewV7()).String() },
	}
	if recorder, ok := repo.(RetrievalRecorder); ok {
		service.retrievals = recorder
	}
	return service
}

func (s *Service) WithEmbedder(embedder Embedder) *Service {
	s.embedder = embedder
	return s
}

func (s *Service) WithRetrievalRecorder(recorder RetrievalRecorder) *Service {
	s.retrievals = recorder
	return s
}

func (s *Service) recordRetrieval(ctx context.Context, event RetrievalEvent) error {
	if s.retrievals == nil {
		return nil
	}
	event.CreatedAt = s.now()
	return s.retrievals.RecordRetrieval(ctx, event)
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
	var err error
	cmd.UseWhen, err = NormalizeUseWhen(cmd.UseWhen)
	if err != nil {
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
	reqHash := mutationRequestHash(cmd.UseWhen, "remember", cmd.SpaceID, hash, strings.TrimSpace(cmd.Reason), mutationMetadataHash(cmd.Topics, cmd.Provenance, cmd.ValidUntil))
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
	unfamiliar, suggestions, err := s.topicAdvice(ctx, cmd.Principal, cmd.SpaceID, cmd.Topics)
	if err != nil {
		return MutationResult{}, err
	}
	if len(unfamiliar) > 0 && !sameStringSet(normalizeTopics(cmd.ConfirmNewTopics), unfamiliar) {
		return MutationResult{UnfamiliarTopics: unfamiliar, TopicSuggestions: suggestions}, ErrUnfamiliarTopics
	}
	mem, err := s.repo.Create(ctx, CreateRecord{
		PrincipalID: cmd.Principal.ID, SpaceID: cmd.SpaceID,
		Title: strings.TrimSpace(cmd.Title), Body: strings.TrimSpace(cmd.Body), UseWhen: cmd.UseWhen,
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
	return MutationResult{Memory: mem, Written: true, UnfamiliarTopics: unfamiliar}, nil
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
	current, err := s.repo.ReadAuthorized(ctx, cmd.MemoryID, cmd.Principal)
	if err != nil {
		return MutationResult{}, err
	}
	if err := s.auth.Require(ctx, cmd.Principal, current.SpaceID, ActionRevise); err != nil {
		return MutationResult{}, err
	}
	if !cmd.UseWhenSet {
		cmd.UseWhen = current.CurrentRevision.UseWhen
	}
	cmd.UseWhen, err = NormalizeUseWhen(cmd.UseWhen)
	if err != nil {
		return MutationResult{}, err
	}
	substantiveUnchanged := sameSubstantiveRevision(current.CurrentRevision, cmd)
	if current.CurrentRevision.UseWhen != "" && !substantiveUnchanged && !cmd.UseWhenSet {
		return MutationResult{}, fmt.Errorf("%w: use_when must be explicitly preserved, replaced, or cleared when substantive fields change", ErrInvalidInput)
	}
	reqHash := mutationRequestHash(cmd.UseWhen, "revise", cmd.MemoryID, fmt.Sprint(cmd.ExpectedRevision), hash, strings.TrimSpace(cmd.Reason), mutationMetadataHash(cmd.Topics, cmd.Provenance, cmd.ValidUntil))
	if result, ok, err := s.repo.FindIdempotent(ctx, cmd.Principal.ID, "revise", cmd.IdempotencyKey, reqHash); err != nil {
		return MutationResult{}, err
	} else if ok {
		return replayed(result), nil
	}
	if current.CurrentRevision.Number != cmd.ExpectedRevision {
		return MutationResult{}, ErrConflict
	}
	currentEmbeddingInput := strings.TrimSpace(current.CurrentRevision.Title + "\n" + current.CurrentRevision.Body)
	nextEmbeddingInput := strings.TrimSpace(cmd.Title + "\n" + cmd.Body)
	var embedding []float32
	reuseEmbeddingRevision := 0
	if currentEmbeddingInput == nextEmbeddingInput {
		reuseEmbeddingRevision = current.CurrentRevision.Number
	} else {
		embedding, err = s.embed(ctx, nextEmbeddingInput)
		if err != nil {
			return MutationResult{}, err
		}
	}
	if duplicate, err := s.repo.FindExact(ctx, cmd.Principal.ID, current.SpaceID, hash, cmd.MemoryID); err != nil {
		return MutationResult{}, err
	} else if duplicate != nil {
		return MutationResult{}, ErrDuplicate
	}
	unfamiliar, suggestions, err := s.topicAdvice(ctx, cmd.Principal, current.SpaceID, cmd.Topics)
	if err != nil {
		return MutationResult{}, err
	}
	if len(unfamiliar) > 0 && !sameStringSet(normalizeTopics(cmd.ConfirmNewTopics), unfamiliar) {
		return MutationResult{UnfamiliarTopics: unfamiliar, TopicSuggestions: suggestions}, ErrUnfamiliarTopics
	}
	mem, err := s.repo.Revise(ctx, ReviseRecord{
		PrincipalID: cmd.Principal.ID, MemoryID: cmd.MemoryID,
		ExpectedRevision: cmd.ExpectedRevision, Title: strings.TrimSpace(cmd.Title),
		Body: strings.TrimSpace(cmd.Body), UseWhen: cmd.UseWhen, Kind: cmd.Kind, Topics: cmd.Topics,
		Provenance: cmd.Provenance, ValidUntil: cmd.ValidUntil,
		Reason: strings.TrimSpace(cmd.Reason), ContentHash: hash,
		IdempotencyKey: cmd.IdempotencyKey, RequestHash: reqHash, Now: s.now(),
		Embedding: embedding, ReuseEmbeddingRevision: reuseEmbeddingRevision,
		PreserveContentUpdatedAt: substantiveUnchanged,
	})
	if err != nil {
		if result, ok, retryErr := s.repo.FindIdempotent(ctx, cmd.Principal.ID, "revise", cmd.IdempotencyKey, reqHash); retryErr == nil && ok {
			return replayed(result), nil
		}
		return MutationResult{}, err
	}
	return MutationResult{Memory: mem, Written: true, UnfamiliarTopics: unfamiliar}, nil
}

func sameSubstantiveRevision(current Revision, next ReviseCommand) bool {
	if current.Title != strings.TrimSpace(next.Title) || current.Body != strings.TrimSpace(next.Body) || current.Kind != next.Kind {
		return false
	}
	if !slices.Equal(current.Topics, next.Topics) || !slices.Equal(current.Provenance, next.Provenance) {
		return false
	}
	if current.ValidUntil == nil || next.ValidUntil == nil {
		return current.ValidUntil == nil && next.ValidUntil == nil
	}
	return current.ValidUntil.Equal(*next.ValidUntil)
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
	return s.read(ctx, principal, memoryID, RetrievalRead)
}

func (s *Service) read(ctx context.Context, principal Principal, memoryID string, operation RetrievalOperation) (Memory, error) {
	memoryValue, err := s.repo.ReadAuthorized(ctx, memoryID, principal)
	if err != nil {
		return Memory{}, err
	}
	if err := s.recordRetrieval(ctx, RetrievalEvent{
		PrincipalID: principal.ID,
		Operation:   operation,
		ResultCount: 1,
		Targets: []RetrievalTarget{{
			MemoryID: memoryValue.ID,
			Revision: memoryValue.CurrentRevision.Number,
			Rank:     1,
		}},
	}); err != nil {
		return Memory{}, err
	}
	return memoryValue, nil
}

func (s *Service) ReadSegment(ctx context.Context, query ReadQuery) (ReadSegmentResult, error) {
	if strings.TrimSpace(query.MemoryID) == "" {
		return ReadSegmentResult{}, fmt.Errorf("%w: memory_id is required", ErrInvalidInput)
	}
	memoryValue, err := s.repo.ReadAuthorized(ctx, query.MemoryID, query.Principal)
	if err != nil {
		return ReadSegmentResult{}, err
	}
	body := memoryValue.CurrentRevision.Body
	result := ReadSegmentResult{
		MemoryID: memoryValue.ID, SpaceID: memoryValue.SpaceID, Status: memoryValue.Status,
		Revision: memoryValue.CurrentRevision.Number, Title: memoryValue.CurrentRevision.Title,
		UseWhen: memoryValue.CurrentRevision.UseWhen, Kind: memoryValue.CurrentRevision.Kind,
		ActorID:           memoryValue.CurrentRevision.ActorID,
		ActorDisplayName:  memoryValue.CurrentRevision.ActorDisplayName,
		RevisionCreatedAt: memoryValue.CurrentRevision.CreatedAt, ValidUntil: memoryValue.CurrentRevision.ValidUntil,
		Reason: memoryValue.CurrentRevision.Reason,
	}
	result.Topics = append(result.Topics, memoryValue.CurrentRevision.Topics[:min(len(memoryValue.CurrentRevision.Topics), 16)]...)
	result.OmittedTopics = len(memoryValue.CurrentRevision.Topics) - len(result.Topics)
	for _, source := range memoryValue.CurrentRevision.Provenance[:min(len(memoryValue.CurrentRevision.Provenance), 4)] {
		locator, truncated := utf8Prefix(source.Locator, 240)
		result.Provenance = append(result.Provenance, ProvenancePreview{Class: source.Class, Locator: locator, LocatorTruncated: truncated, Attribution: source.Attribution})
	}
	for _, association := range memoryValue.HistoricalAssociations[:min(len(memoryValue.HistoricalAssociations), 8)] {
		basis, truncated := utf8Prefix(association.BasisLocator, 240)
		reason, reasonTruncated := utf8Prefix(association.Reason, 240)
		result.HistoricalAssociations = append(result.HistoricalAssociations, HistoricalAssociationPreview{
			AssertionID: association.AssertionID, Relationship: association.Relationship, PrincipalID: association.PrincipalID,
			PrincipalDisplayName: association.PrincipalDisplayName, BasisRevision: association.BasisRevision,
			BasisClass: association.BasisClass, BasisLocator: basis, BasisTruncated: truncated,
			AssertedBy: association.AssertedBy, AssertedByDisplayName: association.AssertedByDisplayName,
			AssertedAt: association.AssertedAt,
			Reason:     reason, ReasonTruncated: reasonTruncated,
		})
	}
	result.OmittedHistoricalAssociations = len(memoryValue.HistoricalAssociations) - len(result.HistoricalAssociations)
	result.OmittedProvenance = len(memoryValue.CurrentRevision.Provenance) - len(result.Provenance)
	lines := strings.Split(body, "\n")
	if query.MaxBytes == 0 {
		query.MaxBytes = 4096
	}
	if query.MaxBytes < 4 || query.MaxBytes > MaxBodyBytes {
		return ReadSegmentResult{}, fmt.Errorf("%w: max_bytes must be between 4 and %d", ErrInvalidInput, MaxBodyBytes)
	}
	if query.StartByte < 0 || (query.Pattern != "" && query.StartByte != 0) {
		return ReadSegmentResult{}, fmt.Errorf("%w: start_byte must be non-negative and cannot be combined with pattern", ErrInvalidInput)
	}
	if query.Pattern == "" {
		if query.StartByte > 0 {
			if query.StartLine != 0 || query.LineCount != 0 || query.StartByte >= len(body) || !utf8.RuneStart(body[query.StartByte]) {
				return ReadSegmentResult{}, fmt.Errorf("%w: start_byte is invalid or combined with line options", ErrInvalidInput)
			}
			end := min(len(body), query.StartByte+query.MaxBytes)
			for end > query.StartByte && end < len(body) && !utf8.RuneStart(body[end]) {
				end--
			}
			startLine := strings.Count(body[:query.StartByte], "\n") + 1
			content := body[query.StartByte:end]
			window := &BodyWindow{StartLine: startLine, EndLine: contentEndLine(startLine, content), TotalLines: len(lines), StartByte: query.StartByte, EndByte: end, TotalBytes: len(body)}
			if end < len(body) {
				window.NextStartByte = end
			}
			result.Content, result.Window = content, window
			return s.finishReadSegment(ctx, query.Principal, result)
		}
		if query.StartLine == 0 {
			query.StartLine = 1
		}
		if query.LineCount == 0 {
			query.LineCount = 80
		}
		if query.StartLine < 1 || query.LineCount < 1 || query.LineCount > 200 {
			return ReadSegmentResult{}, fmt.Errorf("%w: start_line must be positive and line_count must be between 1 and 200", ErrInvalidInput)
		}
		if query.StartLine > len(lines) {
			return ReadSegmentResult{}, fmt.Errorf("%w: start_line is beyond the memory body", ErrInvalidInput)
		}
		end := min(len(lines), query.StartLine-1+query.LineCount)
		startByte := lineByteOffset(lines, query.StartLine-1)
		content := strings.Join(lines[query.StartLine-1:end], "\n")
		content, truncated := utf8Prefix(content, query.MaxBytes)
		window := &BodyWindow{StartLine: query.StartLine, EndLine: contentEndLine(query.StartLine, content), TotalLines: len(lines), StartByte: startByte, EndByte: startByte + len(content), TotalBytes: len(body)}
		if truncated {
			window.NextStartByte = window.EndByte
		} else if end < len(lines) {
			window.NextStartLine = end + 1
		}
		result.Content, result.Window = content, window
		return s.finishReadSegment(ctx, query.Principal, result)
	}
	if len(query.Pattern) > 512 || query.ContextLines < 0 || query.ContextLines > 10 {
		return ReadSegmentResult{}, fmt.Errorf("%w: pattern exceeds 512 bytes or context_lines is outside 0..10", ErrInvalidInput)
	}
	if query.MaxMatches == 0 {
		query.MaxMatches = 20
	}
	if query.MaxMatches < 1 || query.MaxMatches > 50 {
		return ReadSegmentResult{}, fmt.Errorf("%w: max_matches must be between 1 and 50", ErrInvalidInput)
	}
	pattern, err := regexp.Compile(query.Pattern)
	if err != nil {
		return ReadSegmentResult{}, fmt.Errorf("%w: invalid grep pattern", ErrInvalidInput)
	}
	type lineRange struct{ start, end int }
	if query.StartLine == 0 {
		query.StartLine = 1
	}
	if query.StartLine < 1 || query.StartLine > len(lines) {
		return ReadSegmentResult{}, fmt.Errorf("%w: start_line is beyond the memory body", ErrInvalidInput)
	}
	ranges := make([]lineRange, 0)
	for i, line := range lines {
		if i+1 < query.StartLine {
			continue
		}
		if !pattern.MatchString(line) {
			continue
		}
		start, end := max(0, i-query.ContextLines), min(len(lines)-1, i+query.ContextLines)
		if len(ranges) > 0 && start <= ranges[len(ranges)-1].end+1 {
			ranges[len(ranges)-1].end = max(ranges[len(ranges)-1].end, end)
		} else {
			ranges = append(ranges, lineRange{start: start, end: end})
		}
	}
	matches := make([]BodyMatch, 0, min(len(ranges), query.MaxMatches))
	remaining := query.MaxBytes
	consumed := 0
	for _, value := range ranges[:min(len(ranges), query.MaxMatches)] {
		content, truncated := utf8Prefix(strings.Join(lines[value.start:value.end+1], "\n"), remaining)
		matches = append(matches, BodyMatch{StartLine: value.start + 1, EndLine: contentEndLine(value.start+1, content), Content: content, Truncated: truncated})
		remaining -= len(content)
		consumed++
		if truncated {
			result.NextMatchStartByte = lineByteOffset(lines, value.start) + len(content)
		}
		if truncated || remaining == 0 {
			break
		}
	}
	result.Matches = matches
	result.TotalMatchGroups = len(ranges)
	result.OmittedMatchGroups = len(ranges) - consumed
	if consumed < len(ranges) {
		result.NextMatchStartLine = ranges[consumed].start + 1
	}
	return s.finishReadSegment(ctx, query.Principal, result)
}

func (s *Service) finishReadSegment(ctx context.Context, principal Principal, result ReadSegmentResult) (ReadSegmentResult, error) {
	if err := s.recordRetrieval(ctx, RetrievalEvent{
		PrincipalID: principal.ID,
		Operation:   RetrievalReadSegment,
		ResultCount: 1,
		Targets: []RetrievalTarget{{
			MemoryID: result.MemoryID,
			Revision: result.Revision,
			Rank:     1,
		}},
	}); err != nil {
		return ReadSegmentResult{}, err
	}
	return result, nil
}

func contentEndLine(startLine int, content string) int {
	end := startLine + strings.Count(content, "\n")
	if strings.HasSuffix(content, "\n") {
		end--
	}
	return max(startLine, end)
}

func lineByteOffset(lines []string, line int) int {
	offset := 0
	for i := 0; i < line; i++ {
		offset += len(lines[i]) + 1
	}
	return offset
}

func utf8Prefix(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}

func (s *Service) DiscoverMetadata(ctx context.Context, query MetadataQuery) (MetadataResult, error) {
	query.Query = strings.TrimSpace(query.Query)
	if !query.Facet.Valid() || len(query.Query) > 512 || len(query.SpaceIDs) > 100 {
		return MetadataResult{}, fmt.Errorf("%w: invalid metadata query", ErrInvalidInput)
	}
	if query.Limit == 0 {
		query.Limit = 20
	}
	if query.Limit < 1 || query.Limit > 100 {
		return MetadataResult{}, fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalidInput)
	}
	filters := MemoryFilters{Statuses: query.Statuses}
	if err := normalizeFilters(&filters); err != nil {
		return MetadataResult{}, err
	}
	query.Statuses = filters.Statuses
	requested := query.Limit
	cursorKind := "metadata-" + requestHash(string(query.Facet), query.Query, fmt.Sprint(query.SpaceIDs), fmt.Sprint(query.Statuses), fmt.Sprint(requested))[:12]
	var err error
	if query.Cursor != "" {
		query.Offset, err = decodeCursor(cursorKind, query.Cursor)
		if err != nil {
			return MetadataResult{}, err
		}
	}
	readable, err := s.auth.ReadableSpaces(ctx, query.Principal)
	if err != nil {
		return MetadataResult{}, err
	}
	if len(query.SpaceIDs) > 0 {
		query.SpaceIDs = intersectSpaces(query.SpaceIDs, readable)
	} else {
		query.SpaceIDs = readable
	}
	if len(query.SpaceIDs) == 0 {
		return MetadataResult{}, nil
	}
	page, err := s.repo.DiscoverMetadata(ctx, query)
	if err != nil {
		return MetadataResult{}, err
	}
	// Keep discovery safe for agent context even when exact locator values are
	// individually large. The cursor advances only over values actually emitted.
	values := make([]MetadataValue, 0, len(page.Values))
	const responseBudget = 12 << 10
	for _, value := range page.Values {
		candidate := append(values, value)
		raw, _ := json.Marshal(MetadataResult{Values: candidate, TotalCount: page.TotalCount})
		if len(raw) > responseBudget && len(values) > 0 {
			break
		}
		values = candidate
	}
	result := MetadataResult{Values: values, TotalCount: page.TotalCount}
	if len(values) > 0 && query.Offset+len(values) < page.TotalCount {
		result.NextCursor = encodeCursor(cursorKind, query.Offset+len(values))
	}
	return result, nil
}

func (s *Service) topicAdvice(ctx context.Context, principal Principal, spaceID string, topics []string) ([]string, []MetadataValue, error) {
	if len(topics) == 0 {
		return nil, nil, nil
	}
	usage, err := s.repo.TopicUsage(ctx, principal.ID, spaceID, topics)
	if err != nil {
		return nil, nil, err
	}
	unfamiliar := make([]string, 0, len(topics))
	for _, topic := range topics {
		if usage[topic] == 0 {
			unfamiliar = append(unfamiliar, topic)
		}
	}
	if len(unfamiliar) == 0 {
		return nil, nil, nil
	}
	page, err := s.repo.DiscoverMetadata(ctx, MetadataQuery{
		Facet: MetadataTopic, SpaceIDs: []string{spaceID}, Statuses: []Status{StatusActive}, Limit: 8, Principal: principal,
	})
	if err != nil {
		return nil, nil, err
	}
	return unfamiliar, page.Values, nil
}

func (s *Service) Catalog(ctx context.Context, principal Principal, claims []WorkspaceClaim, cursor ...string) (CatalogResult, error) {
	entries, err := s.catalogEntries(ctx, principal, claims)
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
		entries[0].Name = shorten(entries[0].Name, 128)
		entries[0].Slug = shorten(entries[0].Slug, 64)
		if !withinTokenBudget(CatalogResult{Spaces: entries[:1]}, tokenBudget) {
			entries[0].Topics = nil
		}
		if !withinTokenBudget(CatalogResult{Spaces: entries[:1]}, tokenBudget) {
			entries[0].UseWhen = ""
		}
		if !withinTokenBudget(CatalogResult{Spaces: entries[:1]}, tokenBudget) {
			entries[0].Name = shorten(entries[0].Name, 48)
			entries[0].Slug = shorten(entries[0].Slug, 24)
		}
		if !withinTokenBudget(CatalogResult{Spaces: entries[:1]}, tokenBudget) {
			return CatalogResult{}, fmt.Errorf("%w: catalog cannot fit one Space inside the context budget", ErrInvalidInput)
		}
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
	if len(entries) > 0 && keep == 0 {
		return CatalogResult{}, fmt.Errorf("%w: catalog page did not advance", ErrInvalidInput)
	}
	if result.OmittedCount > 0 {
		result.NextCursor = encodeCursor(cursorKind, offset+keep)
	}
	return result, nil
}

// StartupCatalog returns the authorized catalog before response-budget
// pagination. Callers must pass it directly to the bounded startup renderer.
// Keeping this path separate lets exact host-approved cues rise from later
// catalog pages without increasing the emitted startup envelope.
func (s *Service) StartupCatalog(ctx context.Context, principal Principal, claims []WorkspaceClaim) (CatalogResult, error) {
	entries, err := s.catalogEntries(ctx, principal, claims)
	if err != nil {
		return CatalogResult{}, err
	}
	return CatalogResult{Spaces: entries}, nil
}

func (s *Service) catalogEntries(ctx context.Context, principal Principal, claims []WorkspaceClaim) ([]CatalogEntry, error) {
	if len(claims) > 10 {
		return nil, fmt.Errorf("%w: at most 10 workspace claims", ErrInvalidInput)
	}
	for _, claim := range claims {
		if len(claim.Value) > 4096 {
			return nil, fmt.Errorf("%w: workspace claim too long", ErrInvalidInput)
		}
	}
	spaces, err := s.auth.ReadableSpaces(ctx, principal)
	if err != nil {
		return nil, err
	}
	entries, err := s.repo.Catalog(ctx, principal.ID, spaces, claims)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Service) Recall(ctx context.Context, query RecallQuery) ([]Hit, error) {
	result, err := s.RecallPage(ctx, query)
	return result.Hits, err
}

func (s *Service) BrowsePage(ctx context.Context, query BrowseQuery) (BrowseResult, error) {
	query.Topics = normalizeTopics(query.Topics)
	if len(query.SpaceIDs) > 100 || len(query.Topics) > 32 || len(query.Kinds) > 6 {
		return BrowseResult{}, fmt.Errorf("%w: browse filters exceed limits", ErrInvalidInput)
	}
	if err := normalizeFilters(&query.MemoryFilters); err != nil {
		return BrowseResult{}, err
	}
	if err := validateMetadata(query.Topics, nil); err != nil {
		return BrowseResult{}, err
	}
	for _, kind := range query.Kinds {
		if !kind.Valid() {
			return BrowseResult{}, fmt.Errorf("%w: invalid memory kind", ErrInvalidInput)
		}
	}
	if query.Limit == 0 {
		query.Limit = 20
	}
	if query.Limit < 1 || query.Limit > 50 {
		return BrowseResult{}, fmt.Errorf("%w: limit must be between 1 and 50", ErrInvalidInput)
	}
	requested := query.Limit
	cursorKind := "browse-" + requestHash(fmt.Sprint(query.SpaceIDs), fmt.Sprint(query.Kinds), fmt.Sprint(query.Topics), filtersHash(query.MemoryFilters), fmt.Sprint(requested))[:12]
	if query.Cursor != "" {
		updatedAt, memoryID, err := decodeBrowseCursor(cursorKind, query.Cursor)
		if err != nil {
			return BrowseResult{}, err
		}
		query.BeforeUpdatedAt = &updatedAt
		query.BeforeMemoryID = memoryID
	}
	readable, err := s.auth.ReadableSpaces(ctx, query.Principal)
	if err != nil {
		return BrowseResult{}, err
	}
	if len(query.SpaceIDs) > 0 {
		query.SpaceIDs = intersectSpaces(query.SpaceIDs, readable)
	} else {
		query.SpaceIDs = readable
	}
	if len(query.SpaceIDs) == 0 {
		return BrowseResult{}, nil
	}
	query.Limit = requested + 1
	page, err := s.repo.Browse(ctx, query)
	if err != nil {
		return BrowseResult{}, err
	}
	items := page.Items
	more := len(items) > requested
	if more {
		items = items[:requested]
	}
	result := BrowseResult{Items: items, TotalCount: page.TotalCount}
	if more {
		result.NextCursor = encodeBrowseCursor(cursorKind, items[len(items)-1])
	}
	return result, nil
}

func (s *Service) RecallPage(ctx context.Context, query RecallQuery) (RecallResult, error) {
	return s.recallPage(ctx, query, RetrievalRecall)
}

func (s *Service) Reverie(ctx context.Context, principal Principal, cue string) (ReverieResult, error) {
	cue, err := NormalizeReverieCue(cue)
	if err != nil {
		return ReverieResult{}, err
	}
	result, err := s.recallPage(ctx, RecallQuery{Principal: principal, Query: cue, Limit: 5}, RetrievalReverie)
	if err != nil {
		return ReverieResult{}, err
	}
	return ReverieResult{Cue: cue, Reveries: result.Hits, TotalCount: result.TotalCount}, nil
}

func (s *Service) recallPage(ctx context.Context, query RecallQuery, operation RetrievalOperation) (RecallResult, error) {
	query.Query = strings.TrimSpace(query.Query)
	query.Topics = normalizeTopics(query.Topics)
	if query.Query == "" {
		return RecallResult{}, fmt.Errorf("%w: query is required", ErrInvalidInput)
	}
	if len(query.Query) > 4096 || len(query.SpaceIDs) > 100 || len(query.Topics) > 32 || len(query.Kinds) > 6 || len(query.WorkspaceClaims) > 10 {
		return RecallResult{}, fmt.Errorf("%w: recall filters exceed limits", ErrInvalidInput)
	}
	if err := normalizeFilters(&query.MemoryFilters); err != nil {
		return RecallResult{}, err
	}
	if query.Limit == 0 {
		query.Limit = 6
	}
	if query.Limit < 1 || query.Limit > 20 {
		return RecallResult{}, fmt.Errorf("%w: limit must be between 1 and 20", ErrInvalidInput)
	}
	requested := query.Limit
	cursorKind := "recall-" + requestHash(query.Query, fmt.Sprint(query.SpaceIDs), fmt.Sprint(query.WorkspaceClaims), fmt.Sprint(query.Kinds), fmt.Sprint(query.Topics), filtersHash(query.MemoryFilters), fmt.Sprint(requested))[:12]
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
	page, err := s.repo.Recall(ctx, query)
	if err != nil {
		return RecallResult{}, err
	}
	hits := page.Hits
	repositoryMore := len(hits) > requested
	if repositoryMore {
		hits = hits[:requested]
	}
	for i := range hits {
		hits[i].MatchReasons = inferMatchReasons(query.Query, hits[i])
	}
	const tokenBudget = 2000
	buildResult := func(candidate []Hit, more bool) RecallResult {
		result := RecallResult{Hits: candidate, TotalCount: page.TotalCount}
		if more {
			result.NextCursor = encodeCursor(cursorKind, query.Offset+len(candidate))
		}
		return result
	}
	if !withinTokenBudget(buildResult(hits, repositoryMore), tokenBudget) {
		for i := range hits {
			hits[i].Topics = nil
			hits[i].Excerpt = shorten(hits[i].Excerpt, 200)
			hits[i].Title = shorten(hits[i].Title, 256)
		}
	}
	if !withinTokenBudget(buildResult(hits, repositoryMore), tokenBudget) {
		for i := range hits {
			hits[i].Topics = nil
			hits[i].Excerpt = shorten(hits[i].Excerpt, 64)
			hits[i].Title = shorten(hits[i].Title, 64)
		}
	}
	keep := len(hits)
	for keep > 0 {
		more := repositoryMore || keep < len(hits)
		if withinTokenBudget(buildResult(hits[:keep], more), tokenBudget) {
			break
		}
		keep--
	}
	if keep == 0 && len(hits) > 0 {
		// Valid persisted fields always fit after stage-two compaction. Preserve
		// forward cursor progress if a repository implementation violates those
		// field bounds rather than returning a zero-advance page.
		keep = 1
	}
	result := buildResult(hits[:keep], repositoryMore || keep < len(hits))
	targets := make([]RetrievalTarget, 0, len(result.Hits))
	for i, hit := range result.Hits {
		targets = append(targets, RetrievalTarget{MemoryID: hit.MemoryID, Revision: hit.Revision, Rank: i + 1})
	}
	if err := s.recordRetrieval(ctx, RetrievalEvent{
		PrincipalID: query.Principal.ID,
		Operation:   operation,
		Cue:         query.Query,
		ResultCount: result.TotalCount,
		Targets:     targets,
	}); err != nil {
		return RecallResult{}, err
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

type browseCursor struct {
	Kind      string `json:"kind"`
	UpdatedAt int64  `json:"updated_at"`
	MemoryID  string `json:"memory_id"`
}

func encodeBrowseCursor(kind string, item BrowseItem) string {
	raw, _ := json.Marshal(browseCursor{Kind: kind, UpdatedAt: item.UpdatedAt.UnixNano(), MemoryID: item.MemoryID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeBrowseCursor(kind, cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: invalid browse cursor", ErrInvalidInput)
	}
	var value browseCursor
	if err := json.Unmarshal(raw, &value); err != nil || value.Kind != kind || value.UpdatedAt <= 0 {
		return time.Time{}, "", fmt.Errorf("%w: invalid browse cursor", ErrInvalidInput)
	}
	if _, err := uuid.Parse(value.MemoryID); err != nil {
		return time.Time{}, "", fmt.Errorf("%w: invalid browse cursor", ErrInvalidInput)
	}
	return time.Unix(0, value.UpdatedAt).UTC(), value.MemoryID, nil
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
		if strings.TrimSpace(item.Class) == "" {
			return fmt.Errorf("%w: provenance class is required", ErrInvalidInput)
		}
		if len(item.Class) > 80 || len(item.Locator) > 2048 || len(item.Attribution) > 256 {
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

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]bool, len(left))
	for _, value := range left {
		values[value] = true
	}
	for _, value := range right {
		if !values[value] {
			return false
		}
	}
	return true
}

func normalizeFilters(filters *MemoryFilters) error {
	if len(filters.Statuses) == 0 {
		filters.Statuses = []Status{StatusActive}
	}
	if len(filters.Statuses) > 6 || len(filters.ProvenanceClasses) > 32 || len(filters.ProvenanceLocators) > 32 || len(filters.ProvenanceAttributions) > 32 {
		return fmt.Errorf("%w: metadata filters exceed limits", ErrInvalidInput)
	}
	for _, values := range []struct {
		items []string
		limit int
	}{{filters.ProvenanceClasses, 80}, {filters.ProvenanceLocators, 2048}, {filters.ProvenanceAttributions, 256}} {
		for _, value := range values.items {
			if len(value) > values.limit {
				return fmt.Errorf("%w: metadata filter value is too long", ErrInvalidInput)
			}
		}
	}
	seenStatuses := map[Status]bool{}
	statuses := filters.Statuses[:0]
	for _, status := range filters.Statuses {
		if !status.Valid() {
			return fmt.Errorf("%w: invalid memory status", ErrInvalidInput)
		}
		if !seenStatuses[status] {
			seenStatuses[status] = true
			statuses = append(statuses, status)
		}
	}
	filters.Statuses = statuses
	filters.ProvenanceClasses = normalizeValues(filters.ProvenanceClasses, true)
	filters.ProvenanceLocators = exactValues(filters.ProvenanceLocators)
	filters.ProvenanceAttributions = exactValues(filters.ProvenanceAttributions)
	if filters.Revision != nil && *filters.Revision < 1 {
		return fmt.Errorf("%w: revision must be positive", ErrInvalidInput)
	}
	for _, span := range [][2]*time.Time{
		{filters.RememberedFrom, filters.RememberedTo}, {filters.RevisedFrom, filters.RevisedTo}, {filters.ValidFrom, filters.ValidTo},
	} {
		if span[0] != nil && span[1] != nil && span[0].After(*span[1]) {
			return fmt.Errorf("%w: filter range starts after it ends", ErrInvalidInput)
		}
	}
	return nil
}

func normalizeValues(values []string, lower bool) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if lower {
			value = strings.ToLower(value)
		}
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func exactValues(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func filtersHash(filters MemoryFilters) string {
	raw, _ := json.Marshal(filters)
	return string(raw)
}

func NormalizeUseWhen(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: use_when must be valid UTF-8", ErrInvalidInput)
	}
	value = norm.NFC.String(value)
	var out strings.Builder
	pendingSpace := false
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || isUnicodeNoncharacter(r) {
			return "", fmt.Errorf("%w: use_when contains unsafe formatting", ErrInvalidInput)
		}
		if unicode.IsSpace(r) {
			pendingSpace = out.Len() > 0
			continue
		}
		if pendingSpace {
			out.WriteByte(' ')
			pendingSpace = false
		}
		out.WriteRune(r)
	}
	normalized := out.String()
	if len([]byte(normalized)) > MaxUseWhenBytes {
		return "", fmt.Errorf("%w: use_when exceeds %d bytes", ErrInvalidInput, MaxUseWhenBytes)
	}
	return normalized, nil
}

func NormalizeReverieCue(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%w: reverie cue must be valid UTF-8", ErrInvalidInput)
	}
	value = norm.NFC.String(value)
	var out strings.Builder
	pendingSpace := false
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || isUnicodeNoncharacter(r) {
			return "", fmt.Errorf("%w: reverie cue contains unsafe formatting", ErrInvalidInput)
		}
		if unicode.IsSpace(r) {
			pendingSpace = out.Len() > 0
			continue
		}
		if pendingSpace {
			out.WriteByte(' ')
			pendingSpace = false
		}
		out.WriteRune(r)
	}
	normalized := out.String()
	if normalized == "" {
		return "", fmt.Errorf("%w: reverie cue is required", ErrInvalidInput)
	}
	if len([]byte(normalized)) > MaxReverieCueBytes {
		return "", fmt.Errorf("%w: reverie cue exceeds %d bytes", ErrInvalidInput, MaxReverieCueBytes)
	}
	wordCount := len(strings.Fields(normalized))
	if wordCount < MinReverieCueWords || wordCount > MaxReverieCueWords {
		return "", fmt.Errorf("%w: reverie cue must contain %d-%d words", ErrInvalidInput, MinReverieCueWords, MaxReverieCueWords)
	}
	return normalized, nil
}

func inferMatchReasons(query string, hit Hit) []string {
	terms := wordSet(query)
	reasons := make([]string, 0, 3)
	for _, candidate := range []struct {
		name  string
		value string
	}{{"title", hit.Title}, {"use_when", hit.UseWhen}, {"content", hit.Excerpt}} {
		words := wordSet(candidate.value)
		for term := range terms {
			if _, ok := words[term]; ok {
				reasons = append(reasons, candidate.name)
				break
			}
		}
	}
	if len(reasons) == 0 {
		return []string{"hybrid"}
	}
	return reasons
}

func wordSet(value string) map[string]struct{} {
	words := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make(map[string]struct{}, len(words))
	for _, word := range words {
		if word != "" {
			out[word] = struct{}{}
		}
	}
	return out
}

func isUnicodeNoncharacter(r rune) bool {
	return r >= 0xfdd0 && r <= 0xfdef || r&0xffff == 0xfffe || r&0xffff == 0xffff
}

func contentHash(title, body string, kind Kind) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(title)) + "\x00" + strings.TrimSpace(body) + "\x00" + string(kind)))
	return hex.EncodeToString(sum[:])
}

func requestHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func mutationRequestHash(useWhen string, parts ...string) string {
	if useWhen != "" {
		parts = append(parts, "use_when", useWhen)
	}
	return requestHash(parts...)
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
