// pubsub-topology writes the Pub/Sub topology (internal/events/pubsub) as Terraform's variables and as the emulator's init script.
//
// Run it from backend/:
//
//	go run ./cmd/pubsub-topology          # rewrite both files
//	go run ./cmd/pubsub-topology -check   # exit 1 and name each file that differs (CI)
//
// The outputs are infra/gcp/pubsub.auto.tfvars.json (Go's json.MarshalIndent with two
// spaces and one newline, which infra/gcp reads as its pubsub_* variables) and
// deploy/pubsub-emulator/init.sh (POSIX sh and curl that create the same topics and
// subscriptions on compose's emulator, with endpoints http://<service>:8080<path> and
// Terraform's retry and dead-letter policy). Both are committed, so the topology is
// edited in Go and never in the files.
//
// See docs/MESSAGING_DESIGN.md; D218.
package main
