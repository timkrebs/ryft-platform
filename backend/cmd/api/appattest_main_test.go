package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/appattest/appattesttest"
)

// App Attest's start refusals on the api (D247): warn or on without the ring, a malformed ring in
// any mode, and a ring without APPLE_TEAM_ID or APPLE_CLIENT_ID stop it before it connects (the
// database is a closed port), and no error holds key material. A good configuration logs one "app
// attest" line of the mode, whether a ring is set and the exempt count, never a key or an account.
func TestMainAppAttestConfig(t *testing.T) {
	if os.Getenv("RYFT_TEST_MAIN") == "1" {
		main()
		return
	}
	good := appattesttest.RingLine(13, 0x2e)
	key := good[strings.IndexByte(good, ':')+1:]
	exempt := uuid.NewString()
	apple := []string{"APPLE_TEAM_ID=M77SKHKG63", "APPLE_CLIENT_ID=dev.timkrebs.ryft"}
	run := func(wait time.Duration, env ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainAppAttestConfig$")
		cmd.Env = append([]string{"RYFT_TEST_MAIN=1", "DATABASE_URL=postgres://api@127.0.0.1:1/ryft", "PORT=1", "METRICS_PORT=1",
			"AUTH_MODE=apikey", "API_KEY=" + strings.Repeat("0123456789abcdef", 2), "ALLOW_INSECURE_APIKEY_AUTH=true",
			"PRIVACY_POLICY_VERSION=2026-10-26",
			"PUBSUB_EMULATOR_HOST=127.0.0.1:1", "ALLOW_INSECURE_PUSH=true"}, env...) // compose's local Pub/Sub, never reached
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, env := range [][]string{
		append([]string{"APP_ATTEST=warn"}, apple...),
		append([]string{"APP_ATTEST=on"}, apple...),
		append([]string{"APP_ATTEST=off", "APP_ATTEST_TOKEN_KEYS=" + good + "\n" + good}, apple...),
		{"APP_ATTEST=off", "APP_ATTEST_TOKEN_KEYS=" + good, "APPLE_CLIENT_ID=dev.timkrebs.ryft"},
		{"APP_ATTEST=warn", "APP_ATTEST_TOKEN_KEYS=" + good, "APPLE_TEAM_ID=M77SKHKG63"},
	} {
		out, err := run(10*time.Second, env...)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(out, `"msg":"app attest config"`) {
			t.Fatalf("%v: want exit 1 with the app attest config error, got %v:\n%s", env[:1], err, out)
		}
		if strings.Contains(out, `"msg":"listening"`) || strings.Contains(out, key[:12]) {
			t.Errorf("%v: listened or printed key material:\n%s", env[:1], out)
		}
	}
	out, _ := run(3*time.Second, append([]string{"APP_ATTEST=warn", "APP_ATTEST_TOKEN_KEYS=" + good, "APP_ATTEST_EXEMPT_USERS=" + exempt}, apple...)...)
	said := false
	for _, line := range strings.Split(out, "\n") {
		said = said || (strings.Contains(line, `"msg":"app attest"`) && strings.Contains(line, `"mode":"warn"`) &&
			strings.Contains(line, `"ring":true`) && strings.Contains(line, `"exempt":1`))
	}
	if !said || strings.Contains(out, key[:12]) || strings.Contains(out, exempt) {
		t.Fatalf("want one app attest line without the key or the account:\n%s", out)
	}
}
