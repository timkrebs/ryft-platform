package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/timkrebs/ryft/internal/events/pubsub"
)

func main() {
	check := flag.Bool("check", false, "fail when a file differs instead of writing it")
	root := flag.String("root", "..", "the repository root")
	flag.Parse()
	os.Exit(run(*root, *check, os.Stdout, os.Stderr))
}

// output is one generated file.
type output struct {
	rel  string
	data []byte
	mode fs.FileMode // when the file is created
}

func outputs() ([]output, error) {
	t := pubsub.Spec()
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("topology: %w", err)
	}
	tfvars, err := pubsub.TFVars(t)
	if err != nil {
		return nil, err
	}
	return []output{
		{pubsub.TFVarsFile, tfvars, 0o644},
		{pubsub.EmulatorInitFile, pubsub.EmulatorInit(t), 0o755},
	}, nil
}

// run writes the files under root, or with check compares them and names every file
// that differs. It returns the exit code.
func run(root string, check bool, stdout, stderr io.Writer) int {
	files, err := outputs()
	if err != nil {
		fmt.Fprintln(stderr, "✗", err)
		return 1
	}
	var drift []string
	for _, f := range files {
		path := filepath.Join(root, f.rel)
		if check {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, f.data) {
				drift = append(drift, f.rel)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // a directory of the repository
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := os.WriteFile(path, f.data, f.mode); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if len(drift) > 0 {
		slices.Sort(drift)
		for _, rel := range drift {
			fmt.Fprintf(stderr, "✗ %s is not what internal/events/pubsub/topology.go generates: run `go run ./cmd/pubsub-topology` in backend/\n", rel)
		}
		return 1
	}
	if check {
		fmt.Fprintf(stdout, "✓ %s and %s match internal/events/pubsub\n", pubsub.TFVarsFile, pubsub.EmulatorInitFile)
	}
	return 0
}
