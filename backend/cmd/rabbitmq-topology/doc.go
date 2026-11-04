// rabbitmq-topology writes the broker's topology (internal/events/amqpbus) as the
// Topology Operator's objects and as the definitions compose loads:
//
//	go run ./cmd/rabbitmq-topology          # from backend/: rewrite both files
//	go run ./cmd/rabbitmq-topology -check   # fail if either differs (make k8s-validate)
//
// The output is committed, and make k8s-validate fails on drift, so the topology
// is edited in Go and never in the YAML. The dev definitions carry dev passwords
// ("dev-<user>-amqp-password"); it must not hold a production password: on the
// cluster every user's password comes from Vault (Secret amqp-user-<user>).
//
// See docs/MESSAGING_DESIGN.md; D97, D99.
package main
