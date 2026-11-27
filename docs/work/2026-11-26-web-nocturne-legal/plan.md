# ryftlab.de in Nocturne, and legal pages for today's RYFT and the partner programs (2026-11-26)

Brief: head-architect, "ryftlab.de in Nocturne, and legal pages ready for the Garmin and Suunto partner programs".
Base: `origin/main` `4bca49d`. Two draft PRs, no merge, no deploy.

| PR | Branch | Worktree | Owner |
|---|---|---|---|
| A "web: the site in Nocturne" | `web/nocturne` | `.worktrees/web-nocturne` | web-engineer |
| B "web: legal pages for today's RYFT and the partner programs" | `legal/today-and-partners` | `.worktrees/web-legal` | general-purpose (legal writer, needs WebSearch) |

The app source for tokens and design: `.worktrees/app-ea52758`, a clean detached checkout of `timkrebs/ryft-app` at
`ea52758` (app main at #142, after #117 and #131), fetched by SHA into the platform's object store. The local `../app`
was not used: its working tree can't be checked from this session.

## Where the Nocturne spec comes from
No web spec exists. Web D20 (2026-11-17) took only the tokens ("no design change beyond what they carry"); no branch,
draft or `web/docs` file describes a Nocturne site. PR A derives it from the app: ADR 0016,
`docs/work/2026-11-16-nocturne/{brief,design,design-pr6-*}.md`, `Ryft/Design/*.swift`, the shots. It writes the derived
spec to `web/docs/NOCTURNE.md` and says so in the PR.

## Tickets
| # | Ticket | Owner | Acceptance |
|---|---|---|---|
| A1 | Pin `web/.app-ref` to `ea52758`, regenerate tokens (and icons and OG card if the app's icon moved) | web-engineer | `RYFT_APP_DIR=… make web-tokens` twice → no diff; CI `tokens:check` green |
| A2 | `web/docs/NOCTURNE.md`: the site's Nocturne spec, each rule citing the app file it comes from | web-engineer | every rule has a source |
| A3 | Every page in Nocturne: landing, science, beta, support, privacy, terms, Impressum, licences, open, 404, styleguide; header, footer, prose, cards, OG card | web-engineer | web-checks, web-e2e (axe, contrast, Lighthouse budgets) green |
| A4 | Screenshots of every page under a dark and a light system setting; personal details masked | web-engineer | images on the branch, linked in the PR body |
| A5 | web D26, `web/docs/STATUS.md` | web-engineer | |
| B1 | `facts.md`: every fact the texts state, checked on `main` with file:line | legal writer | each row has a source or "not verified" |
| B2 | `partners.md`: Garmin and Suunto public requirements for a partner's site, with URLs; what couldn't be read | legal writer | each requirement has a source or "unverified" |
| B3 | New privacy version `2026-11-26` (based on the C2 draft of #228) + list + `config.env` + compose; terms, support, Impressum updated; no personal details, `TODO(user)` where the law needs them | legal writer | `check:policy --strict`, brand, web-test, web-build, web-e2e green |
| B4 | `LEGAL_CHECKLIST.md` rewritten for today's setup; platform D290; `docs/STATUS.md` | legal writer | |
| R | One code-reviewer pass per PR, on the diff | code-reviewer | findings in `review.md`, blockers fixed |

Files are disjoint. A owns `web/src`, `web/e2e`, `web/tests`, `web/public`, `web/scripts`, `web/.app-ref`, `web/docs/{NOCTURNE,DECISIONS,STATUS}.md`.
B owns `web/content/legal`, `web/docs/LEGAL_CHECKLIST.md`, `deploy/run/config.env`, `docker-compose.yml`,
`docs/{DECISIONS,STATUS}.md`, `docs/work/2026-11-26-legal-partners/`. Decision numbers: A web D26; B platform D290
(D283–D285 are taken on other branches; D286–D289 are left to the parallel sessions).

## Risks
- **Personal details.** On `main` the Impressum, terms §1 and privacy 2026-10-26 §1 carry the user's name, home address
  and phone. B replaces them with `TODO(user)` in the Impressum, the terms and the new privacy version. The old privacy
  version stays as published (it's the record of what people consented to). Merged as it is, B would publish an Impressum
  without a provider, so the user's answer has to land before the merge. Git history keeps the old values either way.
  A's screenshots of those pages mask the details.
- **B supersedes C2 (#228).** The new version builds on C2's text. Once B is merged, #228 should be closed.
- **B's merge rolls the services:** `PRIVACY_POLICY_VERSION` changes, so `deploy.yml` rolls api/ingest/scoring and
  every user is asked to consent again.
- **LCP**: a Nocturne design that adds weights or images can break the 1.5 s budget (web D4, D20).
- The app pin's move can change tokens beyond the design, and CI's `tokens:check` decides.

## Rollback
Revert the PR. For B, revert it before any user has consented to `2026-11-26`. Once someone has, add a later version.

## Needs the user
- The Impressum's and controller's name and address for service (or a business address), the supervisory authority,
  the lawyer's answers (C2's `anwalt.md` questions plus the new ones).
- Merge A, then B (or either alone), each after green CI. `SITE_DEPLOY` on → the merge ships the site.
- Close #228 when B is merged.
