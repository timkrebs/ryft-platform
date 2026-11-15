package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// serve runs Server on a loopback port until the test ends.
func serve(t *testing.T, h http.Handler) (*http.Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := Server(slog.New(slog.DiscardHandler), ln.Addr().String(), h)
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); <-served })
	return srv, "http://" + ln.Addr().String()
}

// h2cClient speaks HTTP/2 with prior knowledge and nothing else, as Cloud Run's front end does with
// --use-http2: a server that only spoke HTTP/1 would fail every request.
func h2cClient() *http.Client {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: &p}, Timeout: bigBodyTimeout}
}

// bigBodyTimeout bounds one 32 MiB request in these tests. Under -race with coverage, CI's runners
// take about 30 s for it (2026-11-15, PR #171), so 30 s was a coin toss; a real hang still fails.
const bigBodyTimeout = 3 * time.Minute

// syncBody is a JSON document of exactly n bytes: {"pad":"aaa…"}.
func syncBody(n int) []byte {
	const open, end = `{"pad":"`, `"}`
	return []byte(open + strings.Repeat("a", n-len(open)-len(end)) + end)
}

// D220: the services speak h2c with prior knowledge (and HTTP/1.1 on the same port), and a sync body of
// exactly MaxBodySync passes HTTP/2's own limits (frames, flow control, streams) while one byte more gets
// our JSON 413, as on HTTP/1.
func TestH2CCarriesAFullSync(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Pad string `json:"pad"`
		}
		if err := ReadJSONStripNUL(w, r, &v, MaxBodySync); err != nil {
			WriteDecodeError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]int{"proto": r.ProtoMajor, "pad": len(v.Pad)})
	})
	_, base := serve(t, h)
	for _, tc := range []struct {
		name   string
		client *http.Client
		size   int
		code   int
		proto  int
	}{
		{"h2c, exactly the limit", h2cClient(), MaxBodySync, http.StatusOK, 2},
		{"h2c, one byte over", h2cClient(), MaxBodySync + 1, http.StatusRequestEntityTooLarge, 0},
		{"HTTP/1.1 on the same port", &http.Client{Timeout: 30 * time.Second}, 1 << 10, http.StatusOK, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tc.client.Post(base+"/v1/sync", "application/json", bytes.NewReader(syncBody(tc.size)))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.code || resp.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("%d %s %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
			}
			if tc.code == http.StatusRequestEntityTooLarge {
				if !strings.Contains(string(body), `"error":"request body over 33554432 bytes"`) || resp.ProtoMajor != 2 {
					t.Errorf("413 body %q over HTTP/%d", body, resp.ProtoMajor)
				}
				return
			}
			var got map[string]int
			if err := json.Unmarshal(body, &got); err != nil || got["proto"] != tc.proto || got["pad"] != tc.size-len(`{"pad":""}`) {
				t.Errorf("answer %s (%v), want proto %d", body, err, tc.proto)
			}
		})
	}
}

// An idle HTTP/2 connection (Cloud Run's front end keeps them open) must not hold the shutdown: after
// GOAWAY it closes within the 2 s app gives the push services' HTTP step (net/http has waited up to a
// second for a GOAWAY to be read in some versions, so 1 s would not be enough).
func TestShutdownWithAnIdleH2CConnection(t *testing.T) {
	srv, base := serve(t, http.HandlerFunc(Live))
	c := h2cClient()
	resp, err := c.Get(base + PathLive)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("HTTP/%d", resp.ProtoMajor)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with an idle h2c connection: %v after %s", err, time.Since(start))
	}
	t.Logf("shutdown took %s", time.Since(start))
}
