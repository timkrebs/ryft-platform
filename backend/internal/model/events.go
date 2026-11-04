package model

import (
	"github.com/google/uuid"
)

// ScoredEvent is published (routing key scored) when a local day first gets a
// recovery after a night with sleep (scoring → notify: the morning push).
type ScoredEvent struct {
	UserID           uuid.UUID `json:"user_id"`
	Day              string    `json:"day"`
	Recovery         int       `json:"recovery"`
	Readiness        *int      `json:"readiness"`
	State            string    `json:"state"`
	SleepPerformance *int      `json:"sleep_performance"`
	SleepMin         int       `json:"sleep_min"`
	RequestID        string    `json:"request_id,omitempty"`
}

// RefreshEvent is published (routing key refresh) when scoring has finished the
// days a sync queued (scoring → notify: a silent push so the app refetches; D92).
type RefreshEvent struct {
	UserID    uuid.UUID `json:"user_id"`
	Day       string    `json:"day"` // the newest day the sync touched
	RequestID string    `json:"request_id,omitempty"`
}

// SessionScoredEvent is published (routing key session.scored) once scoring has
// scored a completed session's day (scoring → notify).
type SessionScoredEvent struct {
	UserID    uuid.UUID `json:"user_id"`
	SessionID uuid.UUID `json:"session_id"`
	Day       string    `json:"day"`
	RequestID string    `json:"request_id,omitempty"`
}

// IngestedEvent reasons that scoring and notify branch on (the others: "recompute",
// "merge", "session"). Both are an explicit list of days, and both end in the silent
// refresh push once scored; an import never carries a session id (D125, D128).
const (
	ReasonSync   = "sync"
	ReasonImport = "import"
)

// IngestedEvent is published (routing key ingested) after a successful sync.
type IngestedEvent struct {
	UserID uuid.UUID `json:"user_id"` // the event is always scoped to one user
	Days   []string  `json:"days"`    // local days whose scores need (re)computing
	Reason string    `json:"reason"`
	// RequestID of the HTTP request that caused the event, so scoring's logs
	// can be correlated with it.
	RequestID string `json:"request_id,omitempty"`
	// SessionID is set for a completed session (session.completed), so
	// scoring can announce it once scored.
	SessionID *uuid.UUID `json:"session_id,omitempty"`
}

// SessionCompletedEvent is published (routing key session.completed) when a
// strength session is completed, so scoring rescores its day.
type SessionCompletedEvent struct {
	UserID    uuid.UUID `json:"user_id"`
	SessionID uuid.UUID `json:"session_id"`
	Day       string    `json:"day"` // local day of the session's start
	RequestID string    `json:"request_id,omitempty"`
}
