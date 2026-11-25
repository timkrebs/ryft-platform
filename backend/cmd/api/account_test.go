package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/apple"
	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/sealed"
	"github.com/timkrebs/ryft/internal/store"
)

// Integration test, opt-in like internal/db's: RYFT_TEST_POSTGRES_URL is a
// superuser connection to a throwaway server.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("RYFT_TEST_POSTGRES_URL")
	if admin == "" {
		t.Skip("set RYFT_TEST_POSTGRES_URL to a throwaway Postgres server")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	// The roles first: tests of other packages migrate on this server at the same time.
	if err := db.EnsureServiceRoles(ctx, root); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ryft_apitest_%d", mrand.Uint32())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String(), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = root.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	})
	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	return pool
}

// fakeApple answers /auth/token with a refresh token for subject and records
// revocations; down makes every call fail.
type fakeApple struct {
	*httptest.Server
	mu      sync.Mutex
	subject string
	revoked []string
	codes   int
	down    bool
}

func newFakeApple(t *testing.T, subject string) *fakeApple {
	f := &fakeApple{subject: subject}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = r.ParseForm()
		if f.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/auth/token":
			f.codes++
			idToken, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
				Issuer: "https://appleid.apple.com", Audience: jwt.ClaimStrings{"dev.timkrebs.ryft"}, Subject: f.subject,
			}).SignedString([]byte("x"))
			_ = json.NewEncoder(w).Encode(map[string]string{"refresh_token": "r.secret-for-" + r.PostForm.Get("code"), "id_token": idToken})
		case "/auth/revoke":
			f.revoked = append(f.revoked, r.PostForm.Get("token"))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeApple) setDown(v bool)      { f.mu.Lock(); f.down = v; f.mu.Unlock() }
func (f *fakeApple) setSubject(v string) { f.mu.Lock(); f.subject = v; f.mu.Unlock() }
func (f *fakeApple) exchanges() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.codes
}
func (f *fakeApple) revocations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

func appleAccounts(t *testing.T, st *store.Store, f *fakeApple) *accounts {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	p8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	c, err := apple.New(apple.Config{TeamID: "T", KeyID: "K", ClientID: "dev.timkrebs.ryft", PrivateKey: p8, BaseURL: f.URL}, f.Client())
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	key, _ := sealed.ParseKey(base64.StdEncoding.EncodeToString(raw))
	return &accounts{st: st, apple: c, key: key, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func asUser(r *http.Request, u model.User) *http.Request {
	ctx := auth.WithUser(auth.WithPrincipal(r.Context(), auth.Principal{UserID: u.ID}), u)
	return r.WithContext(ctx)
}

func TestAppleTokenIsStoredEncryptedAndRevokedOnDelete(t *testing.T) {
	pool := testPool(t)
	st := store.New(pool)
	ctx := context.Background()
	f := newFakeApple(t, "001234.abc")
	h := &handler{st: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), cache: newInsightCache(), accounts: appleAccounts(t, st, f)}
	u, err := st.EnsureUser(ctx, store.Identity{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.EnsureUser(ctx, store.Identity{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	supabase := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.URL.Path {
		case "/admin/users/" + u.ID.String():
			_, _ = io.WriteString(w, `{"identities":[{"provider":"apple","id":"linked-row","identity_data":{"sub":"001234.abc"}}]}`)
		case "/admin/users/" + v.ID.String():
			_, _ = io.WriteString(w, `{"user_metadata":{"iss":"https://appleid.apple.com","provider_id":"001234.abc"},
				"identities":[{"provider":"email","id":"001234.abc"},{"provider":"apple","id":"001234.abc"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(supabase.Close)
	h.accounts.admin, err = auth.NewAdmin(supabase.URL, "sb_secret_fixture", supabase.Client())
	if err != nil {
		t.Fatal(err)
	}
	post := func(account model.User, code string) int {
		rec := httptest.NewRecorder()
		h.appleAuthorization(rec, asUser(httptest.NewRequest(http.MethodPost, "/v1/me/apple-authorization",
			strings.NewReader(`{"code":"`+code+`"}`)), account))
		return rec.Code
	}

	if c := post(v, "c1"); c != http.StatusUnprocessableEntity || f.exchanges() != 0 {
		t.Fatalf("unlinked or forged Apple claims must be refused before exchange, got %d with %d exchanges", c, f.exchanges())
	}
	f.setSubject("999999.someone-else")
	if c := post(u, "c1"); c != http.StatusUnprocessableEntity {
		t.Fatalf("a code for another Apple ID must be refused, got %d", c)
	}
	f.setSubject("")
	if c := post(u, "c1"); c != http.StatusUnprocessableEntity {
		t.Fatalf("a code whose subject is absent must be refused, got %d", c)
	}
	var writes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM apple_tokens`).Scan(&writes); err != nil || writes != 0 {
		t.Fatalf("refused exchanges must not store tokens: got %d rows, error %v", writes, err)
	}
	f.setSubject("001234.abc")
	if c := post(u, "c1"); c != http.StatusNoContent {
		t.Fatalf("store: %d", c)
	}
	var enc []byte
	if err := pool.QueryRow(ctx, `SELECT token_enc FROM apple_tokens WHERE user_id=$1`, u.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "r.secret") {
		t.Fatal("the refresh token must be stored encrypted")
	}

	// Apple is down while the account is deleted: data gone, revocation pending.
	f.setDown(true)
	rec := httptest.NewRecorder()
	h.deleteMe(rec, asUser(httptest.NewRequest(http.MethodDelete, "/v1/me", nil), u))
	var resp AccountDeleted
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != 200 || resp.AppleTokenRevoked == nil || *resp.AppleTokenRevoked {
		t.Fatalf("delete while Apple is down: %d %s", rec.Code, rec.Body.String())
	}

	// The reconciler revokes it once Apple is back, with the token Apple issued.
	f.setDown(false)
	h.accounts.reconcile(ctx)
	if got := f.revocations(); len(got) != 1 || got[0] != "r.secret-for-c1" {
		t.Fatalf("revocations %v", got)
	}
	if pending, _ := st.PendingAppleRevocations(ctx, 10); len(pending) != 0 {
		t.Fatalf("still pending after the reconciler: %v", pending)
	}

	// An email account has nothing to revoke: null, not false.
	rec = httptest.NewRecorder()
	h.deleteMe(rec, asUser(httptest.NewRequest(http.MethodDelete, "/v1/me", nil), v))
	if !strings.Contains(rec.Body.String(), `"apple_token_revoked":null`) {
		t.Fatalf("no Apple token: %s", rec.Body.String())
	}
}
