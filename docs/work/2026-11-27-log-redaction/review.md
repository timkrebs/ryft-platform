# RFT-75 — review (code-reviewer, 2026-11-27)

Scope: `privateLogPath`/`redactPrivatePath` gain `/v1/journal/{day}`, `/v1/days/{day}`, `/v1/coaching/feedback/{date}`.

## Findings
1. **Should fix, fixed.** `ServeMux` redirects an unclean path (`/v1//journal/2026-11-26`, `/v1/x/../days/…`) before
   the `/v1/` handler ran the redactor, so the access line logged the raw date (on #260's routes too). Fix: the
   redactor wraps the whole mux (`newMux` returns `redactPrivatePath(mux)`) and matches the `path.Clean` form too.
   Pinned: `TestPrivateLogPath` cases, and a redirect request per route in `TestPrivatePathsLogNoDateOrWorkoutID`
   (the mux answers 307).
2. **Should fix, documented.** A path that is no route (`/v1/sleep/<date>`, `/v1/Journal/<date>`) is a 404 that logs
   its raw path. Only a broken client sends it. D305 → Not covered.
3. **Nit, documented.** Handlers that set `LogPath = r.Pattern` log the method too (`PUT /v1/coaching/feedback/{date}`).
   No date, so it stays as it is. OPERATIONS → Data protection (logs) now says so.
4. **Nit, accepted.** `slog.SetDefault` in the test is process-global (as in `logs_test.go`); the test is not parallel.

Checked sound: prefix matching (list routes untouched), `%2F` (`r.URL.Path` is decoded, the mux matches the escaped
path), no handler logs `PathValue("day"|"date")`, no other dated route, the test fails without the fix.
