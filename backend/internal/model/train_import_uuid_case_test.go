package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Swift's JSONEncoder writes UUIDs in upper case. Every id of the import request is a
// uuid.UUID, so it parses to the same 16 bytes in either case; every comparison (T10,
// id_taken, newer_on_server) and every query works on those bytes, and a response echoes
// the canonical lower case. This pins that the request types keep their id fields typed so.
func TestImportRequestIDsAreCaseInsensitive(t *testing.T) {
	const body = `{"source":"alpha_progression","plans":[],"sessions":[{
	  "id":"ID","title":"x","started_at":"2026-01-01T10:00:00Z","status":"completed",
	  "plan_day_id":"ID","exercises":[{"id":"ID","exercise_id":"ID","plan_exercise_id":"ID","sets":[]}]}]}`
	const lower = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	build := func(id string) []byte { return []byte(strings.ReplaceAll(body, "ID", id)) }
	var lo, up TrainImportRequest
	if err := json.Unmarshal(build(lower), &lo); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(build(strings.ToUpper(lower)), &up); err != nil {
		t.Fatalf("upper-case ids refused: %v", err)
	}
	if !reflect.DeepEqual(lo, up) {
		t.Fatalf("upper-case ids parse to other values:\n%+v\n%+v", lo, up)
	}
	if lo.Sessions[0].ID == uuid.Nil || lo.Sessions[0].Exercises[0].ExerciseID == uuid.Nil {
		t.Fatal("ids not read")
	}
	out, err := json.Marshal(ImportResult{Kind: ImportKindSession, ID: up.Sessions[0].ID, Status: "created"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"id":"`+lower+`"`) {
		t.Fatalf("results[].id is not lower case: %s", out)
	}
}
