# Nutrition-to-Recovery

N2R version 1.0.0 reports intake, workout context and modeled nutritional effects alongside the original score in the [Nutrition API](../../API.md#nutrition-to-recovery). User settings and nightly feature records are scoped per account. The current consent and the existing Nutrition feature rollout gate computation. Deleting the N2R profile also deletes its nightly records.

## Inputs and score

- The configured `energy_kcal` goal is the base budget. Add energy reported by visible workouts starting on the account's local date. Training duration counts the union of workout intervals within that date. Until workouts have energy estimates, the target uses the configured base. Carb targets are 3.5, 5, 8 and 10 g/kg by strain tier. The score unlocks at 30% of the resulting energy target.
- Recorded workout strain and muscular load use 12 as the high threshold. An explicitly recorded HIIT activity can qualify for the exercise window with missing strain. A high-strain endurance workout of at least 45 minutes is also a proxy for likely glycogen demand. A gap below ten hours promotes the day to Ultra and extends eligible carb coverage to four hours after the qualifying workout. The next session can come from a completed workout or the profile's optional `next_workout_start_at`.
- Use the covered share of added sugars and GI ≥70 carbohydrates in eligible windows. Share each budget across meals and dates and allocate overlaps in earliest-expiration order. Credit sweat sodium only when estimated loss reaches 400 mg. The default sweat model uses 0.8 L/h and 900 mg/L. A missing logged carbohydrate amount leaves its adequacy unknown and known energy can still contribute to underfueling.
- The EPA+DHA depot weights up to 14 calendar days with `alpha = 2/15`. A day contributes when logged calories reach its unlock target and both fatty-acid amounts are complete. Missing observations leave that day's weight out. The other quality components keep their existing evidence requirements. The overall score is nullable while any of the ten components are unknown. Historical daily summaries use the current calorie goal and account settings.
- Alcohol, caffeine and meal composition use saved meal times. Early-morning expected bedtime values from 00:00 through 11:59 belong to the calendar morning following the intake day. Live guidance continues to use that waking day until the expected early-morning bedtime. Completed days can use measured sleep onset. Missing bedtime leaves time-dependent penalties at zero and caffeine residual unavailable. Residual caffeine sums doses at or before bed. Known amounts contribute to the estimate while missing doses remain unmeasured. Strength-day lean protein requires a complete meal fat total below 10 g. The late saturated-fat tag begins at 10% of meal energy. The combined penalty caps at 45 points.
- The CGM windows reuse the Nutrition engine's baseline, spike, iAUC and recovery logic. Multiply the original calorie weight by 0.2 in the standard workout window or 1.5 within four hours of bedtime, with workout context taking precedence. Usable CGM receives 30% of the blended score. The calibration values are a versioned product model. The dose and timing rationale is informed by controlled studies of caffeine and pre-sleep casein. [Caffeine timing trial](https://pubmed.ncbi.nlm.nih.gov/39377163/) · [Pre-sleep casein trial](https://pubmed.ncbi.nlm.nih.gov/22330017/)

## Nightly validation and guidance

The existing 15-minute API tick processes up to 25 eligible wake dates per pass. It uses `daily_scores.hrv_metric = 'rmssd'` for lnRMSSD once typed wearable data is present. RHR uses its own baseline when wearable rows lack RMSSD. Seven complete preceding calendar days establish sample SD, CV and SWC for the current wake day. The signed delta and SWC multipliers describe deviations. RHR uses its own seven-day mean. Each row pairs the previous intake day's features and score with the wake-date observations.

The tuning pass uses up to 30 calendar days and requires 14 paired observations, three repeat signals above 25 mg, and a Pearson correlation of at least 0.4 in the expected direction. A repeat signal is an lnRMSSD decrease of at least 3 SWC or an RHR increase of at least 2 bpm. Use a fixed five-hour reference for historical caffeine exposure. Each new eligible wake date can raise the effective input by 0.5 hours to 7.5 hours. The score exposes the effective value. Associations describe the user's logged and observed data and carry no genetic inference.

The six guidance rules return an ordered card list. Recovery advice uses signed changes in overnight RMSSD and RHR. Optional pre-bed amounts require evidence of elevated sleep latency from overlapping in-bed and sleep-start records. Hydration quantities are per measured workout weight loss. Food results come from the shared catalog. Filters use a single source's nutrient profile per food. Cosine ranking uses existing valid embeddings and an eligible reference food. Live requests make zero calls to the embedding provider.

## Local verification and rollout

```bash
cd plattform/backend
go test ./internal/nutrition/... ./internal/model ./internal/store ./cmd/api
# Set RYFT_TEST_POSTGRES_URL to an isolated, disposable PostgreSQL server.
go test ./internal/db ./internal/store ./cmd/api -run 'N2R|Nutrition' -count=1
# Run this additional integration test when the disposable server has pgvector installed.
RYFT_N2R_TEST_VECTOR=1 go test ./internal/store -run TestN2RPGVectorGuidance -count=1
```

Apply the regular Go migrations through 0046 before serving the new routes. The [ETL runbook](../../../scripts/etl/README.md) covers catalog imports and the optional vector schema. Validate the deployed API and worker on approved staging before rollout.
