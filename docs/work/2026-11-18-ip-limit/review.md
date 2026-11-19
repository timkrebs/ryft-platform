# Review of `backend/ip-limit` (28a42f3), 2026-11-18

One round, code-reviewer, read-only. **No blockers.** It confirmed several points:
- no race on the api's `key`, since `Load` runs before `listen`;
- every `Instrument` caller is updated;
- the 429's bytes and headers match `API.md`;
- `Capped`'s locking and sweep cost are sound, and 6.3 MiB at the cap checks out;
- with a single header line, no forged form is reached at hops 1;
- the exemptions cover the probes and the only push;
- the smoke scripts stay under the limit;
- the render and check scripts are right.

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | `make load` (k6, local) sends no `X-Forwarded-For`, so 50 users would share the fallback bucket, about 90 % `429`, and the threshold fails | should-fix | **fixed**: `scripts/load/day.js` gives each user an address in 198.18.0.0/15; D240 names `make load` |
| 2 | Multi-line `X-Forwarded-For` is read last line first. If Google appended to the first line, a second forged line would buy a new bucket per request, and step 9's index can't tell the two apart | should-fix | **fixed in the runbook**: burst 2b sends two forged lines (a new /64 each time) and must show step 2's split; a stop rule; D240 lists it as unchecked |
| 3 | D240 understated the overflow risk: one IPv6 /48 (65,536 /64s) holds the map full at about 1.1k req/s per instance | should-fix | **fixed**: D240 → Consequences says so. A /48 bucket is a follow-up if seen; Cloud Armor is next |
| 4 | The tick exemption leaves `POST /internal/tick` unthrottled, at the cost of a token check and two lines per request | should-fix | **kept** (the brief exempts what Google pushes; limiting it lets a full map push the real tick into the shared bucket). D240 → Consequences states the cost |
| 5 | The new runbook block has trailing `#` comments, which break in stock zsh | nit | **fixed**: a `setopt interactivecomments` note, as step 10 has |
| 6 | API.md Admin routes: "A preflight is not counted" | nit | **fixed**: not counted by the admin limit, but counted by the per-address limit |
| 7 | D222 and D155 were not marked as amended | nit | **fixed** |
| 8 | `ipIdle` and `ipMaxKeys` were not pinned by any test | nit | **fixed**: `TestEdgeLimitValuesArePinned` also requires idle ≥ the refill time |
| 9 | The `{class="ip"}` series was not created at 0 | nit | **fixed** in `newIPLimit` |

Re-checked after the fixes: `go -C backend vet`, the tests of `internal/app`, `internal/httpx`, `internal/ratelimit` and
`cmd/api`, and `make lint` (0 issues, pkgmap current).
