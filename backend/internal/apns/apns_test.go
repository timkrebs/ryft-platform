package apns

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type fakeAPNs struct {
	*httptest.Server
	key *ecdsa.PrivateKey

	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	expire   bool // answer the next request with ExpiredProviderToken
}

func newFake(t *testing.T) (*fakeAPNs, *Client) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := &fakeAPNs{key: k}
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests, f.bodies = append(f.requests, r), append(f.bodies, string(body))
		expire := f.expire
		f.expire = false
		f.mu.Unlock()
		if r.ProtoMajor != 2 {
			w.WriteHeader(http.StatusHTTPVersionNotSupported)
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "bearer ")
		tok, err := jwt.Parse(bearer, func(*jwt.Token) (any, error) { return &k.PublicKey, nil },
			jwt.WithValidMethods([]string{"ES256"}))
		switch {
		case err != nil || tok.Header["kid"] != "ABCDE12345":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"reason":"InvalidProviderToken"}`)
		case expire:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"reason":"ExpiredProviderToken"}`)
		case strings.HasSuffix(r.URL.Path, "/gone"):
			w.WriteHeader(http.StatusGone)
			_, _ = io.WriteString(w, `{"reason":"Unregistered","timestamp":1}`)
		case strings.HasSuffix(r.URL.Path, "/bad"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"reason":"BadDeviceToken"}`)
		default:
			w.Header().Set("apns-id", "11111111-2222-3333-4444-555555555555")
			w.WriteHeader(http.StatusOK)
		}
	}))
	f.EnableHTTP2 = true
	f.StartTLS()
	t.Cleanup(f.Close)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	p8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	c, err := New(Config{KeyID: "ABCDE12345", TeamID: "TEAMID1234", PrivateKey: p8, Topic: "dev.timkrebs.ryft",
		URLs: map[string]string{Sandbox: f.URL, Production: f.URL}}, f.Client())
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func TestSendAlert(t *testing.T) {
	f, c := newFake(t)
	res, err := c.Send(context.Background(), Notification{Token: "abc123", Env: Sandbox, PushType: PushAlert,
		Payload: []byte(`{"aps":{}}`), CollapseID: "MORNING_SCORE-2026-10-19", Expiration: time.Unix(1800000000, 0)})
	if err != nil || !res.OK() || res.ID == "" {
		t.Fatalf("send: %+v %v", res, err)
	}
	r := f.requests[0]
	for h, want := range map[string]string{"apns-topic": "dev.timkrebs.ryft", "apns-push-type": "alert", "apns-priority": "10",
		"apns-collapse-id": "MORNING_SCORE-2026-10-19", "apns-expiration": "1800000000"} {
		if got := r.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if r.URL.Path != "/3/device/abc123" || f.bodies[0] != `{"aps":{}}` {
		t.Fatalf("request %s %s", r.URL.Path, f.bodies[0])
	}
	if _, err := c.Send(context.Background(), Notification{Token: "x", Env: "staging"}); err == nil {
		t.Fatal("unknown environment")
	}
}

func TestBackgroundPriorityAndGoneTokens(t *testing.T) {
	f, c := newFake(t)
	res, _ := c.Send(context.Background(), Notification{Token: "abc", Env: Production, PushType: PushBackground, Payload: []byte(`{}`)})
	if !res.OK() || f.requests[0].Header.Get("apns-priority") != "5" {
		t.Fatalf("background: %+v priority %s", res, f.requests[0].Header.Get("apns-priority"))
	}
	for path, gone := range map[string]bool{"gone": true, "bad": true} {
		res, err := c.Send(context.Background(), Notification{Token: path, Env: Sandbox, PushType: PushAlert, Payload: []byte(`{}`)})
		if err != nil || res.OK() || res.Gone() != gone || res.Reason == "" {
			t.Errorf("%s: %+v %v", path, res, err)
		}
	}
	if (Result{Status: 400, Reason: "PayloadTooLarge"}).Gone() || (Result{Status: 429}).Gone() {
		t.Fatal("only a dead token is gone")
	}
}

func TestProviderTokenReuseAndRenewal(t *testing.T) {
	f, c := newFake(t)
	now := time.Now()
	c.now = func() time.Time { return now }
	send := func() Result {
		res, err := c.Send(context.Background(), Notification{Token: "abc", Env: Sandbox, PushType: PushAlert, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	send()
	send()
	if a, b := f.requests[0].Header.Get("Authorization"), f.requests[1].Header.Get("Authorization"); a != b {
		t.Fatal("the provider token is reused within 40 minutes")
	}
	now = now.Add(41 * time.Minute)
	send()
	if f.requests[2].Header.Get("Authorization") == f.requests[1].Header.Get("Authorization") {
		t.Fatal("renewed after 40 minutes")
	}
	f.mu.Lock()
	f.expire = true
	f.mu.Unlock()
	if res := send(); !res.OK() || len(f.requests) != 5 {
		t.Fatalf("an expired provider token is renewed and the request repeated once: %+v (%d requests)", res, len(f.requests))
	}
}

func TestNewRefusesBadConfig(t *testing.T) {
	for _, c := range []Config{{KeyID: "short", TeamID: "T", Topic: "x", PrivateKey: "x"},
		{KeyID: "ABCDE12345", TeamID: "T", Topic: "x", PrivateKey: "not a key"}} {
		if _, err := New(c, nil); err == nil {
			t.Errorf("%+v must be refused", c)
		}
	}
}
