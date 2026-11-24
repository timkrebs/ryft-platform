package nutrition

import "github.com/google/uuid"

// ImportStats counts source validation and database write outcomes.
type ImportStats struct {
	Processed   int            `json:"processed"`
	Inserted    int            `json:"inserted"`
	Updated     int            `json:"updated"`
	Unchanged   int            `json:"unchanged"`
	Filtered    int            `json:"filtered"`
	Rejected    int            `json:"rejected"`
	Quarantined int            `json:"quarantined"`
	Warnings    int            `json:"warnings"`
	Valid       int            `json:"valid"`
	Reasons     map[string]int `json:"reasons"`
}

// Add merges a completed batch into cumulative import counters.
func (s *ImportStats) Add(o ImportStats) {
	s.Processed += o.Processed
	s.Inserted += o.Inserted
	s.Updated += o.Updated
	s.Unchanged += o.Unchanged
	s.Filtered += o.Filtered
	s.Rejected += o.Rejected
	s.Quarantined += o.Quarantined
	s.Warnings += o.Warnings
	s.Valid += o.Valid
	if s.Reasons == nil {
		s.Reasons = map[string]int{}
	}
	for k, n := range o.Reasons {
		s.Reasons[k] += n
	}
}

// ImportJob identifies a staged release and its last committed checkpoint.
type ImportJob struct {
	ID          uuid.UUID   `json:"id"`
	Provider    string      `json:"provider"`
	Version     string      `json:"version"`
	Checkpoint  int64       `json:"checkpoint"`
	Counts      ImportStats `json:"counts"`
	Status      string      `json:"status"`
	DatasetHash [32]byte    `json:"-"`
	OptionsHash [32]byte    `json:"-"`
}
