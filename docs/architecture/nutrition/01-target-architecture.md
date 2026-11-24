# Target architecture

Nutrition is a domain inside the Go API. The pure `internal/nutrition` rules validate inputs, normalize search and barcodes, and calculate snapshots. `internal/store` owns SQL and transaction scope. `internal/model` owns the wire types. A one-shot import binary reuses the catalog repository and sits outside API and deploy lifecycles.

| Capability | First release boundary |
| --- | --- |
| Catalog and search | Shared generic and branded foods, owner-only custom foods, translations and aliases, PostgreSQL lexical and trigram search, indexed GTIN candidates and portions |
| Logging | User-reviewed inputs, transactional snapshots, scoped list and summary, goal revisions |
| Ingestion | Versioned source adapters, staged finite files, explicit write mode, reporting and resume |
| Intelligence | Versioned design, see [Nutrition intelligence](09-nutrition-intelligence.md) |
| Recognition and assets | Provider-independent design and future review flow, see [Recognition](08-ai-recognition-design.md) |

Search Food and Barcode Scan both produce a meal draft. Photo, label and text recognition can later populate candidates in the same draft. The confirmation payload holds selected IDs, quantities and basis. The server resolves nutrition and computes coverage. Search is independent of external providers. German and English ranking use records that contain translated labels or synonyms.

## Implementation checklist

1. Add the `nutrition` schema, constrained catalog and personal tables, grants and owner policies.
2. Build normalized source adapters and bounded import transactions with committed checkpoints.
3. Implement scoped search, catalog reads, private creation and submission drafts.
4. Implement idempotent meal creation, revision-checked editing, deletion, daily aggregation and goal revisions.
5. Register dark API routes on existing middleware. Exercise permissions, deterministic fixtures and indexed queries locally.
6. Review licensing, consent, managed telemetry, scale and rollout on staging before enabling release.
