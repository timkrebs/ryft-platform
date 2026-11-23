package fake

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/model"
)

const secret = "unit-test-secret-" + "0123456789"

func TestAuthorizeExchangePKCE(t *testing.T) {
	p := New(secret)
	state, verifier := connect.NewState(), connect.NewVerifier()
	raw, err := p.AuthorizeURL(state, connect.Challenge(verifier), "http://localhost:8080/v1/connect/fake/callback")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path != "/v1/connect/fake/callback" || u.Query().Get("state") != state {
		t.Fatalf("authorize url %q (%v)", raw, err)
	}
	code := u.Query().Get("code")
	g, err := p.Exchange(context.Background(), code, verifier, "")
	if err != nil {
		t.Fatal(err)
	}
	if g.ProviderUserID != UserID(state) || len(g.ProviderUserID) != 16 {
		t.Errorf("provider user id %q, want the first 16 hex of SHA-256(state)", g.ProviderUserID)
	}
	if g.Access == "" || g.Refresh == "" || len(g.Permissions) != 2 || g.RefreshDue.IsZero() {
		t.Errorf("grant %+v", g)
	}
	// A wrong verifier, or a code without its provider id, does not exchange.
	if _, err := p.Exchange(context.Background(), code, connect.NewVerifier(), ""); !errors.Is(err, connect.ErrInvalidGrant) {
		t.Errorf("wrong verifier: %v", err)
	}
	if _, err := p.Exchange(context.Background(), strings.SplitN(code, ".", 2)[0], verifier, ""); !errors.Is(err, connect.ErrInvalidGrant) {
		t.Errorf("code without provider id: %v", err)
	}
	if !p.Called("exchange", g.ProviderUserID) {
		t.Error("the exchange was not recorded")
	}
}

func TestRefreshAndDeregister(t *testing.T) {
	p := New(secret)
	g, err := p.Refresh(context.Background(), "fake-refresh.abc123")
	if err != nil || g.ProviderUserID != "abc123" {
		t.Fatalf("refresh: %+v %v", g, err)
	}
	if _, err := p.Refresh(context.Background(), "nope"); !errors.Is(err, connect.ErrInvalidGrant) {
		t.Errorf("refresh of a foreign token: %v", err)
	}
	if err := p.Deregister(context.Background(), g); err != nil || !p.Called("deregister", "abc123") {
		t.Errorf("deregister: %v, calls %v", err, p.Calls())
	}
	p.DeregisterErr = errors.New("network down")
	if err := p.Deregister(context.Background(), connect.Grant{ProviderUserID: "zzz"}); err == nil || p.Called("deregister", "zzz") {
		t.Errorf("a failing deregister must not record a call: %v", err)
	}
}

func TestVerifyPush(t *testing.T) {
	p := New(secret)
	body := []byte(`{"items":[]}`)
	h := http.Header{}
	h.Set(SignatureHeader, Sign(secret, body))
	if err := p.VerifyPush(h, body, time.Now()); err != nil {
		t.Fatalf("a signed body: %v", err)
	}
	for name, hh := range map[string]http.Header{
		"no header":   {},
		"not hex":     {SignatureHeader: {"zz"}},
		"short":       {SignatureHeader: {"abcd"}},
		"other key":   {SignatureHeader: {Sign("another secret", body)}},
		"other body":  {SignatureHeader: {Sign(secret, []byte(`{"items":[1]}`))}},
		"upper-cased": {SignatureHeader: {strings.ToUpper(Sign("x", body))}},
	} {
		if err := p.VerifyPush(hh, body, time.Now()); !errors.Is(err, connect.ErrUnverified) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

const pushBody = `{"items":[
 {"kind":"sleep","record_id":"n-1","user":"u1","version":3,"night":{"state":"final","segments":[
   {"start":"2026-11-23T22:30:00Z","end":"2026-11-24T00:30:00Z","stage":"light"},
   {"start":"2026-11-24T00:30:00Z","end":"2026-11-24T01:30:00Z","stage":"deep"},
   {"start":"2026-11-24T01:30:00Z","end":"2026-11-24T02:30:00Z","stage":"rem"},
   {"start":"2026-11-24T02:30:00Z","end":"2026-11-24T02:40:00Z","stage":"awake"}]}},
 {"kind":"hrv","record_id":"h-1","user":"u1","version":1,"hrv":{"start":"2026-11-24T02:00:00Z","end":"2026-11-24T02:05:00Z","rmssd_ms":48.5}}]}`

func TestSplitAndParse(t *testing.T) {
	p := New(secret)
	items, err := p.SplitPush([]byte(pushBody))
	if err != nil || len(items) != 2 {
		t.Fatalf("split: %v %v", items, err)
	}
	if items[0].Kind != "sleep" || items[0].RecordID != "n-1" || items[0].ProviderUserID != "u1" || items[0].Version != 3 {
		t.Errorf("head %+v", items[0])
	}
	night, err := p.ParseRecord(items[0].Kind, items[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if night.NightState != model.RecordFinal || len(night.Sleep) != 4 {
		t.Fatalf("night %+v", night)
	}
	// light is core, the other stages keep their names.
	var got []string
	for _, s := range night.Sleep {
		got = append(got, s.Stage)
	}
	if strings.Join(got, ",") != "core,deep,rem,awake" {
		t.Errorf("stages %v", got)
	}
	hrv, err := p.ParseRecord(items[1].Kind, items[1].Payload)
	if err != nil || len(hrv.Samples) != 1 || hrv.Samples[0].Type != model.SampleHRVRMSSD || hrv.Samples[0].Value != 48.5 || hrv.Samples[0].Unit != "ms" {
		t.Fatalf("hrv %+v %v", hrv, err)
	}
}

func TestSplitPushRefusals(t *testing.T) {
	p := New(secret)
	for name, body := range map[string]string{"not json": `{`, "no items": `{"items":[]}`, "item not an object": `{"items":[1]}`,
		"negative version": `{"items":[{"kind":"hrv","record_id":"a","user":"u","version":-1}]}`} {
		if _, err := p.SplitPush([]byte(body)); !errors.Is(err, connect.ErrBadPush) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var many strings.Builder
	many.WriteString(`{"items":[`)
	for i := 0; i <= connect.MaxItems; i++ {
		if i > 0 {
			many.WriteByte(',')
		}
		many.WriteString(`{"kind":"hrv","record_id":"a","user":"u"}`)
	}
	many.WriteString(`]}`)
	if _, err := p.SplitPush([]byte(many.String())); !errors.Is(err, connect.ErrBadPush) {
		t.Errorf("more than MaxItems items: %v", err)
	}
}

func TestParseRecordRefusals(t *testing.T) {
	p := New(secret)
	for name, c := range map[string]struct{ kind, body string }{
		"unknown kind":  {"steps", `{}`},
		"no night":      {"sleep", `{}`},
		"unknown stage": {"sleep", `{"night":{"state":"final","segments":[{"start":"2026-11-24T00:00:00Z","end":"2026-11-24T01:00:00Z","stage":"nap"}]}}`},
		"end before":    {"sleep", `{"night":{"state":"final","segments":[{"start":"2026-11-24T01:00:00Z","end":"2026-11-24T00:00:00Z","stage":"deep"}]}}`},
		"unknown state": {"sleep", `{"night":{"state":"maybe","segments":[{"start":"2026-11-24T00:00:00Z","end":"2026-11-24T01:00:00Z","stage":"deep"}]}}`},
		"zero rmssd":    {"hrv", `{"hrv":{"start":"2026-11-24T00:00:00Z","end":"2026-11-24T00:05:00Z","rmssd_ms":0}}`},
		"huge rmssd":    {"hrv", `{"hrv":{"start":"2026-11-24T00:00:00Z","end":"2026-11-24T00:05:00Z","rmssd_ms":9000}}`},
		"not json":      {"hrv", `[`},
		"no hrv":        {"hrv", `{}`},
	} {
		if _, err := p.ParseRecord(c.kind, json.RawMessage(c.body)); !errors.Is(err, connect.ErrBadRecord) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if r, err := p.ParseRecord("deregistration", json.RawMessage(`{}`)); err != nil || len(r.Sleep)+len(r.Samples) != 0 {
		t.Errorf("deregistration: %+v %v", r, err)
	}
	if r, err := p.ParseRecord("permissions", json.RawMessage(`{"permissions":["read_sleep"]}`)); err != nil || len(r.Permissions) != 1 {
		t.Errorf("permissions: %+v %v", r, err)
	}
}

func TestParseIsPure(t *testing.T) {
	p := New(secret)
	items, _ := p.SplitPush([]byte(pushBody))
	a, _ := p.ParseRecord(items[0].Kind, items[0].Payload)
	b, _ := p.ParseRecord(items[0].Kind, items[0].Payload)
	if len(a.Sleep) != len(b.Sleep) || a.Sleep[0] != b.Sleep[0] {
		t.Fatal("ParseRecord is not deterministic")
	}
}
