package memory

import "time"

const (
	MaxBodyBytes    = 16 * 1024
	MaxUseWhenBytes = 240
)

type Kind string

const (
	KindDirective Kind = "directive"
	KindDecision  Kind = "decision"
	KindFact      Kind = "fact"
	KindProcedure Kind = "procedure"
	KindLesson    Kind = "lesson"
	KindReference Kind = "reference"
)

func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusRetracted, StatusCorrected, StatusSuperseded, StatusExpired, StatusPurged:
		return true
	default:
		return false
	}
}

func (k Kind) Valid() bool {
	switch k {
	case KindDirective, KindDecision, KindFact, KindProcedure, KindLesson, KindReference:
		return true
	default:
		return false
	}
}

type Status string

const (
	StatusActive     Status = "active"
	StatusRetracted  Status = "retracted"
	StatusCorrected  Status = "corrected"
	StatusSuperseded Status = "superseded"
	StatusExpired    Status = "expired"
	StatusPurged     Status = "purged"
)

type Action string

const (
	ActionRead    Action = "read"
	ActionCreate  Action = "create"
	ActionRevise  Action = "revise"
	ActionRetract Action = "retract"
)

type Principal struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name,omitempty"`
}

type Provenance struct {
	Class       string `json:"class" jsonschema:"Source class, such as user_instruction, repository, or web"`
	Locator     string `json:"locator,omitempty" jsonschema:"Optional exact source locator"`
	Attribution string `json:"attribution,omitempty" jsonschema:"Optional unverified source-supplied display name; distinct from the authenticated Actor and never inferred or used for contact data"`
}

// HistoricalAssociation is an operator assertion about a Memory's historical
// relationship to a Principal. It is distinct from both the authenticated
// revision Actor and source Provenance.
type HistoricalAssociation struct {
	AssertionID           string    `json:"assertion_id"`
	Relationship          string    `json:"relationship"`
	PrincipalID           string    `json:"principal_id"`
	PrincipalDisplayName  string    `json:"principal_display_name"`
	BasisRevision         int       `json:"basis_revision"`
	BasisClass            string    `json:"basis_class"`
	BasisLocator          string    `json:"basis_locator,omitempty"`
	AssertedBy            string    `json:"asserted_by"`
	AssertedByDisplayName string    `json:"asserted_by_display_name,omitempty"`
	AssertedAt            time.Time `json:"asserted_at"`
	Reason                string    `json:"reason"`
}

type Revision struct {
	MemoryID    string       `json:"memory_id"`
	Number      int          `json:"number"`
	Title       string       `json:"title"`
	Body        string       `json:"body"`
	UseWhen     string       `json:"use_when,omitempty"`
	Kind        Kind         `json:"kind"`
	Topics      []string     `json:"topics,omitempty"`
	Provenance  []Provenance `json:"provenance"`
	ValidUntil  *time.Time   `json:"valid_until,omitempty"`
	ContentHash string       `json:"content_hash"`
	ActorID     string       `json:"actor_id"`
	// ActorDisplayName is the Principal's current display name. ActorID is the
	// immutable authenticated identity for the write.
	ActorDisplayName string    `json:"actor_display_name,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	Reason           string    `json:"reason"`
}

type Memory struct {
	ID                     string                  `json:"id"`
	SpaceID                string                  `json:"space_id"`
	Status                 Status                  `json:"status"`
	CurrentRevision        Revision                `json:"current_revision"`
	CreatedAt              time.Time               `json:"created_at"`
	HistoricalAssociations []HistoricalAssociation `json:"historical_associations,omitempty"`
}

type DuplicateCandidate struct {
	MemoryID string  `json:"memory_id"`
	Title    string  `json:"title"`
	Excerpt  string  `json:"excerpt"`
	Score    float64 `json:"score"`
	Exact    bool    `json:"exact,omitempty"`
}

type MutationResult struct {
	Memory              Memory               `json:"memory"`
	Written             bool                 `json:"written"`
	Replayed            bool                 `json:"replayed"`
	DuplicateCandidates []DuplicateCandidate `json:"duplicate_candidates,omitempty"`
	UnfamiliarTopics    []string             `json:"unfamiliar_topics,omitempty"`
	TopicSuggestions    []MetadataValue      `json:"existing_topic_examples,omitempty"`
}

type Hit struct {
	MemoryID   string   `json:"memory_id"`
	SpaceID    string   `json:"space_id"`
	Title      string   `json:"title"`
	UseWhen    string   `json:"use_when,omitempty"`
	Kind       Kind     `json:"kind"`
	Topics     []string `json:"topics,omitempty"`
	Excerpt    string   `json:"excerpt"`
	Revision   int      `json:"revision"`
	Score      float64  `json:"score"`
	Provenance string   `json:"provenance"`
}

type WorkspaceClaim struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type CatalogEntry struct {
	SpaceID string   `json:"space_id"`
	Name    string   `json:"name"`
	Slug    string   `json:"slug"`
	Type    string   `json:"type"`
	Topics  []string `json:"topics,omitempty"`
}

type CatalogResult struct {
	Spaces       []CatalogEntry `json:"spaces"`
	OmittedCount int            `json:"omitted_count"`
	NextCursor   string         `json:"next_cursor,omitempty"`
}

type RecallResult struct {
	Hits       []Hit  `json:"hits"`
	TotalCount int    `json:"total_count"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type MetadataFacet string

const (
	MetadataTopic                 MetadataFacet = "topic"
	MetadataProvenanceClass       MetadataFacet = "provenance_class"
	MetadataProvenanceLocator     MetadataFacet = "provenance_locator"
	MetadataProvenanceAttribution MetadataFacet = "provenance_attribution"
)

func (f MetadataFacet) Valid() bool {
	switch f {
	case MetadataTopic, MetadataProvenanceClass, MetadataProvenanceLocator, MetadataProvenanceAttribution:
		return true
	default:
		return false
	}
}

type MetadataValue struct {
	Value       string `json:"value"`
	MemoryCount int    `json:"memory_count"`
}

type MetadataResult struct {
	Values     []MetadataValue `json:"values"`
	TotalCount int             `json:"total_count"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

type MetadataQuery struct {
	Facet     MetadataFacet
	Query     string
	SpaceIDs  []string
	Statuses  []Status
	Limit     int
	Offset    int
	Cursor    string
	Principal Principal
}

type RepositoryMetadataResult struct {
	Values     []MetadataValue
	TotalCount int
}

type BodyWindow struct {
	StartLine     int `json:"start_line"`
	EndLine       int `json:"end_line"`
	TotalLines    int `json:"total_lines"`
	NextStartLine int `json:"next_start_line,omitempty"`
	StartByte     int `json:"start_byte"`
	EndByte       int `json:"end_byte"`
	TotalBytes    int `json:"total_bytes"`
	NextStartByte int `json:"next_start_byte,omitempty"`
}

type BodyMatch struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

type ProvenancePreview struct {
	Class            string `json:"class"`
	Locator          string `json:"locator,omitempty"`
	LocatorTruncated bool   `json:"locator_truncated,omitempty"`
	Attribution      string `json:"attribution,omitempty"`
}

type HistoricalAssociationPreview struct {
	AssertionID           string    `json:"assertion_id"`
	Relationship          string    `json:"relationship"`
	PrincipalID           string    `json:"principal_id"`
	PrincipalDisplayName  string    `json:"principal_display_name"`
	BasisRevision         int       `json:"basis_revision"`
	BasisClass            string    `json:"basis_class"`
	BasisLocator          string    `json:"basis_locator,omitempty"`
	BasisTruncated        bool      `json:"basis_truncated,omitempty"`
	AssertedBy            string    `json:"asserted_by"`
	AssertedByDisplayName string    `json:"asserted_by_display_name,omitempty"`
	AssertedAt            time.Time `json:"asserted_at"`
	Reason                string    `json:"reason"`
	ReasonTruncated       bool      `json:"reason_truncated,omitempty"`
}

type ReadQuery struct {
	Principal    Principal
	MemoryID     string
	StartLine    int
	LineCount    int
	StartByte    int
	MaxBytes     int
	Pattern      string
	ContextLines int
	MaxMatches   int
}

type ReadSegmentResult struct {
	MemoryID                      string                         `json:"memory_id"`
	SpaceID                       string                         `json:"space_id"`
	Status                        Status                         `json:"status"`
	Revision                      int                            `json:"revision"`
	Title                         string                         `json:"title"`
	UseWhen                       string                         `json:"use_when,omitempty"`
	Kind                          Kind                           `json:"kind"`
	ActorID                       string                         `json:"actor_id"`
	ActorDisplayName              string                         `json:"actor_display_name,omitempty"`
	RevisionCreatedAt             time.Time                      `json:"revision_created_at"`
	Topics                        []string                       `json:"topics,omitempty"`
	OmittedTopics                 int                            `json:"omitted_topics,omitempty"`
	Provenance                    []ProvenancePreview            `json:"provenance,omitempty"`
	OmittedProvenance             int                            `json:"omitted_provenance,omitempty"`
	HistoricalAssociations        []HistoricalAssociationPreview `json:"historical_associations,omitempty"`
	OmittedHistoricalAssociations int                            `json:"omitted_historical_associations,omitempty"`
	ValidUntil                    *time.Time                     `json:"valid_until,omitempty"`
	Reason                        string                         `json:"reason"`
	Content                       string                         `json:"content,omitempty"`
	Window                        *BodyWindow                    `json:"body_window,omitempty"`
	Matches                       []BodyMatch                    `json:"matches,omitempty"`
	TotalMatchGroups              int                            `json:"total_match_groups,omitempty"`
	OmittedMatchGroups            int                            `json:"omitted_match_groups,omitempty"`
	NextMatchStartLine            int                            `json:"next_match_start_line,omitempty"`
	NextMatchStartByte            int                            `json:"next_match_start_byte,omitempty"`
}

// BrowseItem is the compact representation used by the human-facing memory
// browser. Unlike a recall Hit it is ordered by recency rather than relevance.
type BrowseItem struct {
	MemoryID  string    `json:"memory_id"`
	SpaceID   string    `json:"space_id"`
	Title     string    `json:"title"`
	Kind      Kind      `json:"kind"`
	Topics    []string  `json:"topics,omitempty"`
	Excerpt   string    `json:"excerpt"`
	Revision  int       `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BrowseResult struct {
	Items      []BrowseItem `json:"items"`
	TotalCount int          `json:"total_count"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type RepositoryBrowseResult struct {
	Items      []BrowseItem
	TotalCount int
}

type RepositoryRecallResult struct {
	Hits       []Hit
	TotalCount int
}

type MemoryFilters struct {
	Statuses               []Status
	Revision               *int
	ProvenanceClasses      []string
	ProvenanceLocators     []string
	ProvenanceAttributions []string
	RememberedFrom         *time.Time
	RememberedTo           *time.Time
	RevisedFrom            *time.Time
	RevisedTo              *time.Time
	ValidFrom              *time.Time
	ValidTo                *time.Time
}

type BrowseQuery struct {
	MemoryFilters
	SpaceIDs        []string
	Kinds           []Kind
	Topics          []string
	Limit           int
	Principal       Principal
	Cursor          string
	BeforeUpdatedAt *time.Time
	BeforeMemoryID  string
}

type RecallQuery struct {
	MemoryFilters
	Query           string
	SpaceIDs        []string
	WorkspaceClaims []WorkspaceClaim
	Kinds           []Kind
	Topics          []string
	Limit           int
	Principal       Principal
	Embedding       []float32
	Cursor          string
	Offset          int
}
