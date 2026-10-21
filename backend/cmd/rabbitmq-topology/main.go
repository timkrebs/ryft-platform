// rabbitmq-topology writes the broker's topology (internal/events/amqpbus) as the
// Topology Operator's objects and as the definitions compose loads:
//
//	go run ./cmd/rabbitmq-topology          # from backend/: rewrite both files
//	go run ./cmd/rabbitmq-topology -check   # fail if either differs (make k8s-validate)
//
// The dev definitions carry dev passwords ("dev-<user>-amqp-password"); on the cluster
// every user's password comes from Vault (Secret amqp-user-<user>).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/timkrebs/ryft/internal/events/amqpbus"
)

func main() {
	check := flag.Bool("check", false, "fail when the files differ instead of writing them")
	root := flag.String("root", "..", "the repository root")
	flag.Parse()
	t := amqpbus.Spec(1)
	if err := t.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "✗ topology:", err)
		os.Exit(1)
	}
	users := map[string]string{"admin": "dev-admin-amqp-password"}
	for _, g := range t.Grants {
		users[g.User] = "dev-" + g.User + "-amqp-password"
	}
	defs, err := json.MarshalIndent(amqpbus.Definitions(t, users), "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	files := map[string][]byte{
		"deploy/rabbitmq/topology/topology.yaml": []byte(amqpbus.CRs(t)),
		"deploy/rabbitmq/dev/definitions.json":   append(defs, '\n'),
	}
	failed := false
	for rel, want := range files {
		path := filepath.Join(*root, rel)
		if *check {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				fmt.Fprintf(os.Stderr, "✗ %s is not what internal/events/amqpbus/topology.go generates: run `go run ./cmd/rabbitmq-topology` in backend/\n", rel)
				failed = true
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // a directory of the repository
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil { //nolint:gosec // a file of the repository, read by everyone
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if failed {
		os.Exit(1)
	}
	if *check {
		fmt.Println("✓ deploy/rabbitmq/topology and dev/definitions.json match internal/events/amqpbus")
	}
}
