package notify

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// D125, the second guard: an imported session is never announced, whatever the event
// (scoring sends no session.scored for an import; a re-complete of an imported session
// would carry its id). It is counted as result import; an app session still gets its push.
func TestImportedSessionIsNeverAnnounced(t *testing.T) {
	now := time.Date(2026, 10, 19, 19, 0, 0, 0, berlin)
	ended, strain := now.Add(-20*time.Minute), 11.2
	notice := store.SessionNotice{Title: "Lower", Status: model.SessionCompleted, EndedAt: &ended, Strain: &strain,
		Origin: model.OriginImport, PRs: []store.PRNotice{{ExerciseEN: "Back squat", ExerciseDE: "Kniebeuge", Kind: model.PRE1RM, Value: 120}}}
	ev := events.NotifyMessage{SessionScored: &model.SessionScoredEvent{UserID: uuid.New(), SessionID: uuid.New(), Day: "2026-10-19"}}
	imported := metrics.PushNotifications.WithLabelValues(model.NotifySessionScored, "import")

	for _, prefs := range []model.NotificationPrefs{
		{},
		{Categories: map[string]bool{model.NotifySessionScored: false}}, // records only: still nothing
	} {
		s, st, ap := service(now, prefs)
		st.session = notice
		before := testutil.ToFloat64(imported)
		if err := s.Handle(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
		if len(ap.sent) != 0 || len(st.claims) != 0 {
			t.Fatalf("an imported session was announced (%+v): %+v %v", prefs, ap.sent, st.claims)
		}
		if n := testutil.ToFloat64(imported) - before; n != 1 {
			t.Fatalf("counted %v times as import, want 1", n)
		}
		// A redelivery changes nothing.
		if err := s.Handle(context.Background(), ev); err != nil || len(ap.sent) != 0 {
			t.Fatalf("redelivered: %v %+v", err, ap.sent)
		}
	}

	s, st, ap := service(now, model.NotificationPrefs{})
	st.session = notice
	st.session.Origin = model.OriginApp
	if err := s.Handle(context.Background(), ev); err != nil || len(ap.sent) != 2 {
		t.Fatalf("an app session is announced: %v %+v", err, ap.sent)
	}
}
