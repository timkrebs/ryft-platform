// Package smoke owns the production smoke after a deploy, run from inside the
// cluster.
//
// Argo CD runs it as the PostSync hook of the ryft-platform application
// (`/app smoke` in the migrate image, namespace ryft-smoke). It checks that every service is ready, signs the dedicated smoke user in, sends
// one tiny sync and waits until scoring has rescored that day with the model the
// api serves, which proves api, ingest, the broker and scoring end to end. Its
// only input is that sync (today's step count) and the smoke user's current
// consent; it must not touch any other account.
//
// scripts/smoke_prod.sh does the same through the public edge and also probes the
// gates; that one stays the operator's check from outside.
//
// See docs/OPERATIONS.md; D95.
package smoke
