package notify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/apns"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// apnsHost is a fake APNs environment over TLS and HTTP/2: it records the
// device tokens it got and answers 410 Unregistered for tokens starting "dead".
type apnsHost struct {
	*httptest.Server
	mu     sync.Mutex
	tokens []string
}

func newAPNsHost(t *testing.T) *apnsHost {
	t.Helper()
	h := &apnsHost{}
	h.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		token := strings.TrimPrefix(r.URL.Path, "/3/device/")
		h.mu.Lock()
		h.tokens = append(h.tokens, token)
		h.mu.Unlock()
		if strings.HasPrefix(token, "dead") {
			w.WriteHeader(http.StatusGone)
			_, _ = io.WriteString(w, `{"reason":"Unregistered","timestamp":1}`)
			return
		}
		w.Header().Set("apns-id", uuid.NewString())
		w.WriteHeader(http.StatusOK)
	}))
	h.EnableHTTP2 = true
	h.StartTLS()
	t.Cleanup(h.Close)
	return h
}

func (h *apnsHost) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.tokens...)
}

// Each token goes to its own environment's host through the real APNs client,
// and a token production calls gone is dropped (TestFlight builds use
// production).
func TestPushEnvironmentsReachTheirHosts(t *testing.T) {
	sandbox, production := newAPNsHost(t), newAPNsHost(t)
	roots := x509.NewCertPool()
	roots.AddCert(sandbox.Certificate())
	roots.AddCert(production.Certificate())
	httpClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	client, err := apns.New(apns.Config{KeyID: "ABCDE12345", TeamID: "TEAMID1234", Topic: "dev.timkrebs.ryft",
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		URLs:       map[string]string{apns.Sandbox: sandbox.URL, apns.Production: production.URL}}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	rec, ready := 82, 78
	st := &fakeStore{claims: map[string]int{}, morning: store.Morning{Recovery: &rec, Readiness: &ready, State: model.StateBalanced},
		recipient: store.Recipient{ID: uuid.New(), Loc: berlin, Tokens: []model.PushToken{
			{DeviceID: uuid.New(), Token: "aaaa01", Env: apns.Sandbox, Locale: "en"},
			{DeviceID: uuid.New(), Token: "bbbb02", Env: apns.Production, Locale: "de"},
			{DeviceID: uuid.New(), Token: "dead03", Env: apns.Production, Locale: "de"},
		}}}
	s := &Service{St: st, APNs: client, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return time.Date(2026, 10, 19, 8, 0, 0, 0, berlin) }}
	if err := s.Handle(context.Background(), morningEvent("2026-10-19")); err != nil {
		t.Fatal(err)
	}
	// The morning alert and the silent refresh: two requests per live device,
	// one for the dead token (dropped after the first answer).
	count := func(tokens []string) map[string]int {
		m := map[string]int{}
		for _, tok := range tokens {
			m[tok]++
		}
		return m
	}
	if got := count(sandbox.got()); len(got) != 1 || got["aaaa01"] != 2 {
		t.Errorf("the sandbox host got %v, want only the sandbox token", got)
	}
	if got := count(production.got()); len(got) != 2 || got["bbbb02"] != 2 || got["dead03"] != 1 {
		t.Errorf("the production host got %v, want the production tokens", got)
	}
	if len(st.dropped) != 1 || st.dropped[0] != "dead03" {
		t.Errorf("410 Unregistered drops the token: %v", st.dropped)
	}
	if st.claims[key("2026-10-19", model.NotifyMorningScore)] != 2 {
		t.Errorf("sent to the two live devices: %v", st.claims)
	}
}
