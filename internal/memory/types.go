package memory

import "time"

const MaxBodyBytes = 16 * 1024

type Kind string

const (
	KindDirective Kind = "directive"
	KindDecision  Kind = "decision"
	KindFact      Kind = "fact"
	KindProcedure Kind = "procedure"
	KindLesson    Kind = "lesson"
	KindReference Kind = "reference"
)

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
	ID   string
	Type string
}

type Provenance struct {
	Class   string `json:"class"`
	Locator string `json:"locator,omitempty"`
}

type Revision struct {
	MemoryID    string       `json:"memory_id"`
	Number      int          `json:"number"`
	Title       string       `json:"title"`
	Body        string       `json:"body"`
	Kind        Kind         `json:"kind"`
	Topics      []string     `json:"topics,omitempty"`
	Provenance  []Provenance `json:"provenance"`
	ValidUntil  *time.Time   `json:"valid_until,omitempty"`
	ContentHash string       `json:"content_hash"`
	ActorID     string       `json:"actor_id"`
	CreatedAt   time.Time    `json:"created_at"`
	Reason      string       `json:"reason"`
}

type Memory struct {
	ID              string    `json:"id"`
	SpaceID         string    `json:"space_id"`
	Status          Status    `json:"status"`
	CurrentRevision Revision  `json:"current_revision"`
	CreatedAt       time.Time `json:"created_at"`
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
}

type Hit struct {
	MemoryID   string   `json:"memory_id"`
	SpaceID    string   `json:"space_id"`
	Title      string   `json:"title"`
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
	NextCursor string `json:"next_cursor,omitempty"`
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
	NextCursor string       `json:"next_cursor,omitempty"`
}

type BrowseQuery struct {
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
