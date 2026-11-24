# Nutrition intelligence and health integration

## Score architecture

Version `1.0.0` is a RYFT product metric inspired by AHEI-2010. The original index evaluates dietary patterns. RYFT applies its own thresholds to tracked meals on a local day. [Chiuve et al., AHEI-2010](https://doi.org/10.3945/jn.111.157222)

### Food quality

Unlock the ten components at 30% of the latest configured `energy_kcal` goal. Sum saved kcal values, using kJ divided by 4.184 only when kcal is absent. The threshold uses snapshot precision of six decimal places. Missing goals or a subtotal below the gate leave quality and final scores null. Reaching the gate describes recorded energy coverage. Each component needs complete inputs among that day's logged items. Missing inputs leave that component null and withhold the overall quality score. An explicit zero is a known value.

| Component | Optimal | Low-score anchor |
| --- | ---: | ---: |
| Non-starchy vegetables | 300 g | 0 g |
| Whole fruit | 200 g | 0 g |
| Fiber | 30 g | 0 g |
| Nuts and legumes | 30 g | 0 g |
| EPA plus DHA | 250 mg | 0 mg |
| Unsaturated fat percent of energy | 10% | 2% earns 2 points under the specified positive formula |
| Added sugar including juice and soft drinks | 10 g | 50 g |
| Red or processed meat | 0 g | 110 g |
| Sodium | 1,500 mg | 3,500 mg |
| Trans fat percent of energy | 0.5% | 4% |

Positive points are `min(10, 10 × amount / optimum)`. Negative points linearly decline from 10 at the optimum to 0 at the poor anchor and clamp to that interval. Multiply the mean of the ten values by 10. Both fat energy percentages use 9 kcal per gram. Unsaturated fat combines the mono- and polyunsaturated values. Juice and sugary drinks use their recorded total sugar, with no double count of added sugar. Other foods use recorded added sugar.

Catalog owners can enter explicit contributions in `nutrition.classifications` for `system='ryft_nutrition'` and `version='1'` using the five `quality_profile` keys documented by the API. The most complete source profile wins, with source ID as a stable tie-break. Use 0 or 1 for the `sugary_drink` row. Exclude potato and corn from vegetables, exclude juice from whole fruit and use the combined edible fraction for red or processed meat. Private users set their profile when creating custom foods. The meal item snapshots those values at creation or explicit recalculation. Existing snapshots without a profile stay unknown. Shared source imports need a curated classification pass before their records can support full scoring.

### Glucose impact and final score

Use each meal's absolute start time and a 120-minute window, cut at the next distinct meal time even across local date boundaries. Simultaneous meals share the trace. Window endpoints are inclusive. A window needs three distinct CGM timestamps. The baseline is the median in the inclusive 15 minutes through meal start, or the first in-window reading when premeal readings are absent. Compute the peak delta, trapezoidal area of per-sample positive differences in minutes, and the positive difference at the last in-window reading. No end extrapolation is applied. Results belong to the meal's saved local date, including when its window crosses midnight.

Linearly map the peak delta 20–60 mg/dL, iAUC 1,000–4,500 mg/dL·min and recovery delta 5–30 mg/dL from 100 to 0, clamped. Meal weights are 50%, 35%, and 15%. The day weights evaluable meals by known positive kcal and still reports individual scores for evaluable meals with missing calories. The final score is quality alone or 70% quality and 30% glucose when the day has an evaluable CGM average. Quality must be available to calculate a final score.

The optional `chrononutrition=true` subtracts five points, floored at zero, if strictly more than 25% of fully known logged kcal are late. Use the next wake day's recorded main sleep onset when available and count meals strictly under two hours before it. Otherwise use meal timestamps after 21:00 in each meal's saved timezone. The endpoint computes on demand in a consistent database snapshot.

## Integration

- **Journal.** Derive late meals and late caffeine relative to actual sleep onset when supported. Use versioned timing and dose rules for alcohol, meal timing, fiber and added sugar. Missing logs remain unknown. Create separate, source-attributed derived events tied to meal IDs. Recompute or retract them after an edit, date shift or deletion. Preserve user-edited journal entries.
- **Sleep, recovery and strain.** Align an exposure interval using consumption instants and the next sleep episode's actual timestamps, then compare within person to their baseline. Require adequate observations, represent missingness and selection bias, check confounders including alcohol, activity, travel and existing sleep patterns, and show uncertainty. Correlations are observational.
- **HealthKit.** Reuse per-account consent and anchored-query lifecycle only after adding distinct dietary sample handling in the App. Start with one-way export of user-confirmed RYFT entries after explicit type-level permissions. Use stable sync IDs and versions per meal item/nutrient, writer/source attribution and unit normalization. RYFT owns its exports, updates or deletes its own records, and suppresses reimport of them. An optional later import assigns external source authority to HealthKit samples, stores external UUIDs plus deletion events, and keeps a user review path for conflicts. Store only supported, known nutrients.
- **Glucose sources.** The existing Nutrition endpoint accepts CGM batches. Automatic provider synchronization, exercise adjustment and more advanced handling of gaps remain future integrations. Product summaries are observational.
