package notify

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/apns"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// notifyLogKeys is every line the service logs, with the kind of each value
// (docs/OPERATIONS.md → Data protection (logs)): which push went where, never
// the score in it, the readiness, a device token or an APNs error's free text.
var notifyLogKeys = logcheck.Lines{
	"push": {"category": categories, "day": logcheck.Day, "devices": logcheck.Count, "of": logcheck.Count},
	"apns refused": {"category": categories, "status": logcheck.IntBetween(100, 599),
		"reason": logcheck.Pattern(apnsReason), "device_id": logcheck.UUID},
	"sync stale":            {"err": logcheck.Text(256)},
	"release claim":         {"err": logcheck.Text(256)},
	"record send":           {"err": logcheck.Text(256)},
	"record push delivered": {"err": logcheck.Text(256)},
	"drop push token":       {"err": logcheck.Text(256)},
	"refresh push":          {"err": logcheck.Text(256)},
}

var (
	notifyStandardKeys = logcheck.Standard("notify")
	categories         = logcheck.Enum(model.NotifyMorningScore, model.NotifySessionScored, model.NotifyPR, model.NotifySyncStale, Refresh)
	apnsReason         = regexp.MustCompile(`^[A-Za-z]{1,64}$`)
)

// logStore fails the writes after a send on demand.
type logStore struct {
	*fakeStore
	failDelivered, failDrop, failSent, failRelease bool
}

var errDB = errors.New("db down")

func (s *logStore) PushDelivered(ctx context.Context, token string) error {
	if s.failDelivered {
		return errDB
	}
	return s.fakeStore.PushDelivered(ctx, token)
}
func (s *logStore) DropPushToken(ctx context.Context, token string) error {
	if s.failDrop {
		return errDB
	}
	return s.fakeStore.DropPushToken(ctx, token)
}
func (s *logStore) NotificationSent(ctx context.Context, u uuid.UUID, day, category string, devices int) error {
	if s.failSent {
		return errDB
	}
	return s.fakeStore.NotificationSent(ctx, u, day, category, devices)
}
func (s *logStore) ReleaseNotification(ctx context.Context, u uuid.UUID, day, category string) error {
	if s.failRelease {
		return errDB
	}
	return s.fakeStore.ReleaseNotification(ctx, u, day, category)
}

// logAPNs refuses the tokens in refuse with a non-final 403.
type logAPNs struct {
	*fakeAPNs
	refuse map[string]bool
}

func (a *logAPNs) Send(ctx context.Context, n apns.Notification) (apns.Result, error) {
	if a.refuse[n.Token] {
		return apns.Result{Status: 403, Reason: "InvalidProviderToken"}, nil
	}
	return a.fakeAPNs.Send(ctx, n)
}

// A morning score, a silent refresh and the stale check, with every failure
// path the service logs (a refused push, a pruned token, the writes after a
// send, an unreachable APNs), log which category went to how many devices and
// nothing of the person: not the recovery (82, and 80 in the event), not the
// readiness (78), not a device token. The tokens are built at run time.
func TestNotifyLogsNoHealthValuesOrTokens(t *testing.T) {
	hex := func() string { return strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "") }
	live, dead, refused := "live"+hex(), "dead"+hex(), "refused"+hex()
	toks := []model.PushToken{
		{DeviceID: uuid.New(), Token: live, Env: apns.Sandbox, Locale: "en"},
		{DeviceID: uuid.New(), Token: dead, Env: apns.Sandbox, Locale: "de"},
		{DeviceID: uuid.New(), Token: refused, Env: apns.Production, Locale: "en"},
	}
	var buf bytes.Buffer
	log := logx.NewWriter(&buf, "notify", slog.LevelDebug)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, berlin)

	// build a service on the shared fakes: a fresh user, the three tokens
	build := func() (*Service, *logStore, *logAPNs) {
		s, st, ap := service(now, model.NotificationPrefs{})
		st.recipient.Tokens = toks
		ls, la := &logStore{fakeStore: st}, &logAPNs{fakeAPNs: ap, refuse: map[string]bool{refused: true}}
		s.St, s.APNs, s.Log = ls, la, log
		return s, ls, la
	}
	ctx := logx.WithFields(context.Background(), &logx.Fields{RequestID: uuid.NewString()})

	// 1. everything works, and one device refuses
	s, ls, _ := build()
	ls.failDelivered, ls.failDrop, ls.failSent = true, true, true
	if err := s.Handle(ctx, morningEvent("2026-10-19")); err != nil {
		t.Fatal(err)
	}
	// 2. APNs down and the release fails
	s, ls, la := build()
	la.down, ls.failRelease = true, true
	if err := s.Handle(ctx, morningEvent("2026-10-19")); err == nil {
		t.Fatal("APNs unreachable: the event is retried")
	}
	// 3. a refresh to an unreachable APNs
	s, st, la := build()
	la.down = true
	if err := s.Handle(ctx, events.NotifyMessage{Refresh: &model.RefreshEvent{UserID: st.recipient.ID, Day: "2026-10-19"}}); err != nil {
		t.Fatal(err)
	}
	// 4. the stale check to an unreachable APNs
	s, st, la = build()
	la.down = true
	old := now.Add(-21 * time.Hour)
	stale := st.recipient
	stale.LastSample = &old
	st.stale = []store.Recipient{stale}
	if err := s.CheckStale(ctx); err != nil {
		t.Fatal(err)
	}

	forbid := logcheck.Forbidden{
		Texts:   []string{live, dead, refused},
		Numbers: []float64{82, 78, 80},
	}
	lines, problems := logcheck.Check(buf.String(), notifyStandardKeys, notifyLogKeys, forbid)
	for _, p := range problems {
		t.Error(p)
	}
	// every line the service writes was reached, or the test proves nothing
	for msg := range notifyLogKeys {
		if len(lines[msg]) == 0 {
			t.Errorf("no %q line: the scenario no longer reaches it", msg)
		}
	}
	// the silent refresh goes first and prunes the dead token: the morning
	// alert then reaches one of the two that are left
	if p := lines["push"]; len(p) == 0 || p[0]["category"] != Refresh || p[0]["devices"] != 1.0 || p[0]["of"] != 2.0 {
		t.Errorf("the refresh reached one of two devices: %v", p)
	}
	if r := lines["apns refused"]; len(r) == 0 || r[0]["status"] != 403.0 || r[0]["reason"] != "InvalidProviderToken" ||
		r[0]["device_id"] != toks[2].DeviceID.String() {
		t.Errorf("the refusal names the device by its id: %v", r)
	}
}

// The checker rejects, for the key it mutates, what these lines could leak.
func TestNotifyLogKindsRejectMutations(t *testing.T) {
	const stamp = `"time":"2026-10-23T07:38:47.3Z","level":"WARN","service":"notify"`
	user, device := uuid.NewString(), uuid.NewString()
	line := func(msg, rest string) string {
		return `{` + stamp + `,"msg":"` + msg + `","user_id":"` + user + `",` + rest + `}`
	}
	pushOK := `"category":"MORNING_SCORE","day":"2026-10-19","devices":2,"of":3`
	refusedOK := `"category":"REFRESH","status":403,"reason":"InvalidProviderToken","device_id":"` + device + `"`
	forbid := logcheck.Forbidden{Texts: []string{"tok-secret"}, Numbers: []float64{82, 78}}

	logcheck.Accepts(t, notifyStandardKeys, notifyLogKeys, forbid, line("push", pushOK))
	logcheck.Accepts(t, notifyStandardKeys, notifyLogKeys, forbid, line("apns refused", refusedOK))
	logcheck.Accepts(t, notifyStandardKeys, notifyLogKeys, forbid, line("refresh push", `"err":"connection refused"`))

	key := logcheck.Key
	for _, m := range []struct{ name, line, want string }{
		{"recovery as devices", line("push", strings.Replace(pushOK, `"devices":2`, `"devices":82`, 1)), "value 82"},
		{"float as devices", line("push", strings.Replace(pushOK, `"devices":2`, `"devices":82.5`, 1)), key("devices")},
		{"readiness under a new key", line("push", pushOK+`,"readiness":78`), key("readiness")},
		{"score under a new key", line("push", pushOK+`,"recovery":71`), key("recovery")},
		{"category outside the enum", line("push", strings.Replace(pushOK, `"MORNING_SCORE"`, `"recovery 82"`, 1)), key("category")},
		{"day that is not a day", line("push", strings.Replace(pushOK, `"2026-10-19"`, `"82"`, 1)), key("day")},
		{"free text as the reason", line("apns refused", strings.Replace(refusedOK, `InvalidProviderToken`, `bad token tok-secret`, 1)), key("reason")},
		{"a token as the device", line("apns refused", strings.Replace(refusedOK, device, "tok-secret", 1)), key("device_id")},
		{"float status", line("apns refused", strings.Replace(refusedOK, `403`, `82.5`, 1)), key("status")},
		{"a token in an error", line("refresh push", `"err":"send to tok-secret failed"`), `holds "tok-secret"`},
		{"user_id twice, a score first", line("push", `"user_id":82.5,`+pushOK), `key "user_id" twice`},
		{"non-UUID user_id", `{` + stamp + `,"msg":"push","user_id":"u-82",` + pushOK + `}`, key("user_id")},
		{"unknown message", line("morning score sent", pushOK), "a new log line"},
	} {
		logcheck.Rejects(t, notifyStandardKeys, notifyLogKeys, forbid, m.name, m.line, m.want)
	}
}
