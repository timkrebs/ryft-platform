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
			idToken, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: f.subject}).SignedString([]byte("x"))
			_ = json.NewEncoder(w).Encode(map[string]string{"refresh_token": "r.secret-for-" + r.PostForm.Get("code"), "id_token": idToken})
		case "/auth/revoke":
			f.revoked = append(f.revoked, r.PostForm.Get("token"))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeApple) setDown(v bool) { f.mu.Lock(); f.down = v; f.mu.Unlock() }
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

func asUser(r *http.Request, u model.User, appleSub string) *http.Request {
	ctx := auth.WithUser(auth.WithPrincipal(r.Context(), auth.Principal{UserID: u.ID, AppleSubject: appleSub}), u)
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
	post := func(code, sub string) int {
		rec := httptest.NewRecorder()
		h.appleAuthorization(rec, asUser(httptest.NewRequest(http.MethodPost, "/v1/me/apple-authorization",
			strings.NewReader(`{"code":"`+code+`"}`)), u, sub))
		return rec.Code
	}

	if c := post("c1", "999999.someone-else"); c != http.StatusUnprocessableEntity {
		t.Fatalf("a code for another Apple ID must be refused, got %d", c)
	}
	if c := post("c1", "001234.abc"); c != http.StatusNoContent {
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
	h.deleteMe(rec, asUser(httptest.NewRequest(http.MethodDelete, "/v1/me", nil), u, "001234.abc"))
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
	v, _ := st.EnsureUser(ctx, store.Identity{ID: uuid.New()})
	rec = httptest.NewRecorder()
	h.deleteMe(rec, asUser(httptest.NewRequest(http.MethodDelete, "/v1/me", nil), v, ""))
	if !strings.Contains(rec.Body.String(), `"apple_token_revoked":null`) {
		t.Fatalf("no Apple token: %s", rec.Body.String())
	}
}
