// Package smoke owns the Go end-to-end smoke against a running stack.
//
// CI runs it against the compose stack after `make smoke` (`make smoke-hook`,
// `migrate smoke` in the migrate image). It checks that every service is ready,
// signs the dedicated smoke user in (or takes SMOKE_TOKEN, as compose does), sends
// one tiny sync and waits until scoring has rescored that day with the model the
// api serves, which proves api, ingest, the broker and scoring end to end. Its
// only input is that sync (today's step count) and the smoke user's current
// consent; it must not touch any other account.
//
// scripts/smoke_prod.sh (`make smoke-prod`, which deploy.yml runs after the services
// roll) does the same against production through the public edge and also probes
// the gates.
//
// See docs/OPERATIONS.md; D95.
package smoke
