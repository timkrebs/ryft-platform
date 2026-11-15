package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timkrebs/ryft/internal/events/pubsub"
)

// The committed files are what the topology generates (the CI check).
func TestRepositoryIsCurrent(t *testing.T) {
	var out, errs bytes.Buffer
	if code := run("../../..", true, &out, &errs); code != 0 {
		t.Fatalf("-check exits %d: %s", code, errs.String())
	}
	if !strings.HasPrefix(out.String(), "✓ ") {
		t.Errorf("stdout %q", out.String())
	}
}

// Without the files -check fails and names both; writing creates them (init.sh
// executable); then -check passes; a changed file is named alone.
func TestWriteAndCheck(t *testing.T) {
	root := t.TempDir()
	var out, errs bytes.Buffer
	if code := run(root, true, &out, &errs); code == 0 {
		t.Fatal("-check passes without the files")
	}
	for _, rel := range []string{pubsub.TFVarsFile, pubsub.EmulatorInitFile} {
		if !strings.Contains(errs.String(), "✗ "+rel+" ") {
			t.Errorf("stderr does not name %s:\n%s", rel, errs.String())
		}
	}

	errs.Reset()
	if code := run(root, false, &out, &errs); code != 0 {
		t.Fatalf("writing exits %d: %s", code, errs.String())
	}
	info, err := os.Stat(filepath.Join(root, pubsub.EmulatorInitFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("init.sh is %v, not executable", info.Mode())
	}
	out.Reset()
	if code := run(root, true, &out, &errs); code != 0 || errs.Len() != 0 {
		t.Fatalf("-check after writing exits %d: %s", code, errs.String())
	}

	tfvars := filepath.Join(root, pubsub.TFVarsFile)
	raw, err := os.ReadFile(tfvars)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tfvars, bytes.TrimSuffix(raw, []byte("\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run(root, true, &out, &errs); code != 1 {
		t.Fatalf("-check with a changed tfvars exits %d", code)
	}
	if !strings.Contains(errs.String(), pubsub.TFVarsFile) || strings.Contains(errs.String(), pubsub.EmulatorInitFile) {
		t.Errorf("stderr names the wrong files:\n%s", errs.String())
	}
}
