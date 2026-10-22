package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// F1: without PRIVACY_POLICY_VERSION the api exits 1 before it connects to
// anything. The database is a closed port: were the check behind db.Open, the
// start-up retries would outlast the timeout. The child gets only this
// environment, so nothing from the developer's shell reaches config.Load or
// LoadAuth; the version is unset, as in a pod without it.
func TestMainRefusesMissingPolicyBeforeConnecting(t *testing.T) {
	if os.Getenv("RYFT_TEST_MAIN") == "1" {
		main()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainRefusesMissingPolicyBeforeConnecting$")
	cmd.Env = []string{"RYFT_TEST_MAIN=1", "DATABASE_URL=postgres://api@127.0.0.1:1/ryft",
		"AUTH_MODE=apikey", "API_KEY=0123456789abcdef", "ALLOW_INSECURE_APIKEY_AUTH=true"}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), `"msg":"beta config"`) ||
		!strings.Contains(string(out), "PRIVACY_POLICY_VERSION is required: ") {
		t.Fatalf("want exit 1 with the beta config error, got %v:\n%s", err, out)
	}
}
