package model

import (
	"time"

	"github.com/google/uuid"
)

// The training import, POST /v1/train/import (docs/API.md, D124-D129).

// Import sources: the request enum of TrainImportRequest.source, closed; a new one
// arrives additively.
const ImportSourceAlphaProgression = "alpha_progression"

var KnownImportSources = map[string]bool{ImportSourceAlphaProgression: true}

// Import result kinds and statuses (ImportResult), closed in v1. "exercise" is reserved.
const (
	ImportKindPlan    = "plan"
	ImportKindSession = "session"
)

// TrainImportRequest is the body of POST /v1/train/import.
type TrainImportRequest struct {
	Source   string          `json:"source"`
	Plans    []Plan          `json:"plans"`
	Sessions []ImportSession `json:"sessions"`
}

// ImportSession is a Session of an import request: the same fields, without origin and
// import_source, which the endpoint decides (D125). ReadJSON refuses unknown fields, so a
// request that carries either is a 400. A test keeps the two field lists in step.
type ImportSession struct {
	ID            uuid.UUID         `json:"id"`
	Title         string            `json:"title"`
	StartedAt     time.Time         `json:"started_at"`
	EndedAt       *time.Time        `json:"ended_at"`
	Status        string            `json:"status"`
	PlanDayID     *uuid.UUID        `json:"plan_day_id"`
	WorkoutID     *uuid.UUID        `json:"workout_id"`
	Notes         string            `json:"notes"`
	BodyweightKG  *float64          `json:"bodyweight_kg"`
	MuscularTRIMP *float64          `json:"muscular_trimp"`
	Strain        *float64          `json:"strain"`
	VolumeKG      *float64          `json:"volume_kg"`
	DeviceID      *uuid.UUID        `json:"device_id"`
	Exercises     []SessionExercise `json:"exercises"`
	PRs           []PR              `json:"prs"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// ToSession is the session the request describes. The fields the server owns or computes
// (workout_id, muscular_trimp, strain, volume_kg, prs, updated_at) are ignored on input,
// as PUT /v1/sessions/{id} ignores them, so they are not copied.
func (s ImportSession) ToSession() Session {
	return Session{ID: s.ID, Title: s.Title, StartedAt: s.StartedAt, EndedAt: s.EndedAt, Status: s.Status,
		PlanDayID: s.PlanDayID, Notes: s.Notes, BodyweightKG: s.BodyweightKG, DeviceID: s.DeviceID,
		Exercises: s.Exercises, PRs: []PR{}}
}

// ImportResult is what happened to one item of the request.
type ImportResult struct {
	Kind    string    `json:"kind"`
	ID      uuid.UUID `json:"id"`
	Status  string    `json:"status"`
	Code    string    `json:"code,omitempty"`    // rejected only
	Message string    `json:"message,omitempty"` // rejected only: ids and field names, never values (D114)
}

// TrainImportResponse is the 200 of POST /v1/train/import.
type TrainImportResponse struct {
	Results     []ImportResult `json:"results"` // plans, then sessions, in request order
	DaysTouched []string       `json:"days_touched"`
	Scoring     SyncScoring    `json:"scoring"`
}
