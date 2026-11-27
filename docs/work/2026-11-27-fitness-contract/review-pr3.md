# Review: C1 PR 3, the new sync types behind sync.fitness_types (2026-11-27)

One code-reviewer pass over `feat/sync-fitness-types` (Codex's draft plus the lead's changes). No blockers.

| # | Severity | Finding | Outcome |
| --- | --- | --- | --- |
| 1 | should-fix | The forced consent re-check records its refusal under `mode="on"` even when `CONSENT_ENFORCE` is off or warn. | Fixed: a refusal after the forced re-check is counted under the configured mode. |
| 2 | should-fix | Direct connections (`screenRecord`, `cmd/ingest/connect.go`) accept every type in `KnownSampleTypes`, now including the two new types, with no Fitness gate. | Fixed: connections reject the three gated types until D304 covers them. Test added. |
| 3 | nit | A gated sample outside the window is counted `outside_window`, not `unknown_type`, as on the pilot path. | D306 says so. API.md unchanged. |
| 4 | nit | `heart_rate_recovery` → `hr` suppresses the "no heart rate source" insight for a source that sends only recovery samples. | D306 says so. Such a source records heart rate too. |
| 5 | nit | The pilot's body-mass syncs now make one more lookup (the flag). | Accepted: the migration runs before the services roll. |

Checked and sound: tenant isolation, cursor-only requests, `was_user_entered`, the pilot path, the counts, idempotency,
migration 0069 (rerunnable, no `feature_flags` grant for ingest, Supabase revoke), the seeded-flag test, and no health
values in logs.
