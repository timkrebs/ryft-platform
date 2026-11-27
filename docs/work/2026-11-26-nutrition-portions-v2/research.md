# Research: nutrition portions v2 (G3 OFF portions, G4 German names)

2026-11-26. Every fact comes from a search snippet; no page was opened (WebFetch was refused). That is why every source carries "(search)". Estimates are labelled as estimates. This is not legal advice.

## G3 · OFF supermarket portions

### 1. How many products

| Country tag | Products | Date |
|---|---|---|
| en:germany | ~389,000 (mirrors show 381,709 to 389,468) | undated, probably 2025/26 [OFF countries](https://world.openfoodfacts.org/countries) (search), [API mirror](https://ssl-api.openfoodfacts.org/countries) (search) |
| en:germany (dashboard) | ~250,000 "sold in Germany", 36 % with a Nutri-Score | Nov 2024 [Dashboard/Germany via Progressboard](https://wiki.openfoodfacts.org/Progressboard/Germany) (search) |
| en:switzerland | ~101,000 | undated [OFF countries](https://world.openfoodfacts.org/countries) (search) |
| en:austria | ~20,000 (looks low, so check it) | undated [OFF countries](https://world.openfoodfacts.org/countries) (search) |

- The German count grew about 2.1 % a month in late 2024 [Progressboard/Germany](https://wiki.openfoodfacts.org/Progressboard/Germany) (search).
- I found no published count of German products that have nutrition per 100 g. The Nutri-Score needs nutrition facts plus a category, so 36 % is a lower bound for "has nutrition" [Dashboard](https://wiki.openfoodfacts.org/Country_Support_-_Germany) (search).
- The Parquet export has `no_nutrition_data`, `nutrition_data_per` and `completeness`, so the exact number is one query on the file we already download [HF product-database](https://huggingface.co/datasets/openfoodfacts/product-database) (search).
- **Working estimate:** 150k to 300k importable DE+AT+CH products after removing duplicates (the countries overlap heavily). This is an estimate. Run the DuckDB count on the Parquet file before deciding.

### 2. Size in Supabase (estimate)

Postgres rows carry a 23 B tuple header, aligned to 24 B, plus a 4 B line pointer. Pages are 8 KB with a 24 B header [PG storage page layout](https://www.postgresql.org/docs/18/storage-page-layout.html) (search). I count about 28 B of overhead per row plus the data.

| Table | Rows/product (lo / mid / hi) | B/row (incl. 28 B overhead) | B/product (lo / mid / hi) |
|---|---|---|---|
| foods | 1 | ~130 (uuid, kind, refs, timestamps) | 130 |
| food_sources | 1 | ~150 (source, external id, url, fetched_at) | 150 |
| food_names | 1 / 2 / 3 | ~130 (lang, label ~40, normalized ~40) | 130 / 260 / 390 |
| nutrient_values | 12 / 16 / 20 | ~72 to 80 | 860 / 1,220 / 1,600 |
| barcodes | 1 | ~70 | 70 |
| portions | 0 / 1.5 / 3 | ~85 | 0 / 130 / 255 |
| classifications | 2 / 6 / 10 | ~85 | 170 / 510 / 850 |
| **Heap** | **18 / 28.5 / 39 rows** | | **~1.5 / 2.5 / 3.4 KB** |
| **+ indexes** (×1 / ×1.5 / ×2) | | | **~3.0 / 6.2 / 10.3 KB** |

| Scenario | Products | Rows | Size |
|---|---|---|---|
| Low | 150k | 2.7 M | 150k × 3.0 KB ≈ **0.45 GB** |
| Mid | 220k | 6.3 M | 220k × 6.2 KB ≈ **1.4 GB** |
| High | 300k | 11.7 M | 300k × 10.3 KB ≈ **3.1 GB** |

Plan limits: Free gives 500 MB of database per project. Pro costs $25/month and includes 8 GB of disk, then $0.125/GB [Supabase pricing](https://supabase.com/pricing) (search), [billing docs](https://supabase.com/docs/guides/platform/billing-on-supabase) (search).

- **Free does not fit.** Even the low case plus the existing catalogue, auth and WAL is over 500 MB.
- **Pro fits.** The mid case uses about 17 % of 8 GB, and the high case about 40 %.
- A full re-import by upsert can roughly double table size until vacuum runs. Keep at least 2× headroom.
- The largest tables are `nutrient_values` and a trigram/GIN index on `food_names.normalized`. Skipping null nutrients and keeping only leaf categories in `classifications` are the cheapest savings.

### 3. Licences: ODbL, DbCL, images (flag for a lawyer)

- **Split:** OFF licenses the database under the ODbL, the individual contents under the DbCL and the product images under CC BY-SA [OFF reuse docs](https://openfoodfacts.github.io/openfoodfacts-server/api/tutorials/license-be-on-the-legal-side) (search), [OFF terms mirror](https://world-zh.openfoodfacts.org/terms-of-use) (search). One source gives the image licence as CC BY-SA 3.0 [OFF ODBL wiki](https://wiki.openfoodfacts.org/ODBL_License) (search).
- **Attribution (OFF's ask):** Show "(c) Open Food Facts contributors" with a link to OFF or its terms on each page or screen where the data appears, on the App Store listing and on the app's legal page [OFF ODBL wiki](https://wiki.openfoodfacts.org/ODBL_License) (search), [OFF terms mirror](https://si.openfoodfacts.org/terms-of-use) (search).
- **ODbL notice:** Public use of the database needs a licence notice. Public use of a **Produced Work** needs a notice saying the content comes from the database, for example "Contains information from Open Food Facts, which is made available here under the Open Database License (ODbL)", with a link to the licence [ODbL text mirror](https://wking.github.io/license-list-data/dev/website/ODbL-1.0.html) (search), [Interoperable Europe](https://interoperable-europe.ec.europa.eu/licence/odc-open-database-license-odbl) (search), [ODC summary](https://www.opendatacommons.org/licenses/odbl/summary/) (search).
- **Derivative vs Collective:**
  - A *Derivative Database* is the database or a substantial part of it after any translation, adaptation, arrangement or modification.
  - A *Collective Database* holds the database **in unmodified form** next to independent databases, and is not a Derivative [ODbL mirror](https://physionet.org/about/licenses/open-data-commons-open-database-license-v10/) (search).
  - Our OFF slice is filtered (DACH), reshaped into our schema and enriched with parsed portions. That is a modification, so the **OFF-derived rows are very likely a Derivative Database**.
  - USDA and BLS rows sitting beside them, kept apart by `food_sources`, can plausibly be called a Collective Database. That claim needs a lawyer.
  - OFF's own reading is stricter: it treats mixing OFF data with your own data as something that must be shared back [OFF ODBL non-compliance](https://wiki.openfoodfacts.org/ODBL_non-compliance) (search).
- **Share-alike (4.4/4.6):** If we publicly use a Derivative Database, or a Produced Work made from one, we must offer recipients a copy of the derivative under the ODbL. The offer can be either the full database in machine-readable form or the file or method that turns the original into ours. We do not have to publish it unasked, but the offer must exist. Internal use is exempt [OSM-legal-talk on 4.6](https://lists.openstreetmap.org/pipermail/legal-talk/2020-October/008968.html) (search), [OSM alteration files guideline](https://wiki.openstreetmap.org/wiki/Open_Data_License/Alteration_Files_-_Guideline) (search).
- **App screens:** Produced Works are outputs such as images and text made from the contents; the OSMF treats rendered outputs that way [OSMF Produced Work guideline](https://osmfoundation.org/wiki/Community_Guidelines/Produced_Work_-_Guideline) (search).
  - A screen showing one product is plausibly a Produced Work and needs the notice.
  - An API that answers search and barcode queries from our catalogue looks more like public use of the Derivative Database itself.
  - Either way the 4.6 offer applies, because the source is a derivative. **Lawyer question:** does a query API count as "Publicly Use" of the database? Also, does our own user data (logs, favourites that point to OFF rows) stay outside the derivative?
- **Images:** CC BY-SA covers only the photo and its contributor's rights. Logos or packaging shown in a photo may carry third-party rights [OFF terms mirror](https://si.openfoodfacts.org/terms-of-use) (search). Recommendation: **do not import images** in v2.
- **Warranty:** OFF data comes as is, with no accuracy guarantee [OFF ODBL wiki](https://wiki.openfoodfacts.org/ODBL_License) (search). Show OFF values as "from the package, community-sourced".

### 4. Portion parser sketch

OFF fields to use:
- `quantity`: the free text on the package.
- `product_quantity` and `product_quantity_unit`: OFF's normalised total. "2 x 60 g" gives 120, and the type is sometimes a string.
- `serving_size`: free text.
- `serving_quantity`: OFF's grams or ml parsed from `serving_size`.
- `categories_tags`.

Sources: [OFF product schema](https://openfoodfacts.github.io/documentation/docs/Product-Opener/schemas/schemas/product/) (search), [OFF data fields](https://wiki.openfoodfacts.org/Data_fields) (search).

| Step | Input | Rule | Output |
|---|---|---|---|
| 1 normalise | `quantity` | lowercase; turn decimal `,` into `.`; `×`/`*` into `x`; drop `e` (℮); split on `=`, so "20 x 21 g = 420 g" keeps the left side and checks it against the right | clean string |
| 2 match | clean | `^(?:(\d+)\s*x\s*)?(\d+(?:\.\d+)?)\s*(g\|kg\|ml\|cl\|l)$`, with kg→g ×1000, l→ml ×1000, cl→ml ×10 | `count` (default 1), `unit_amount`, `unit` |
| 3 cross-check | `product_quantity`(+unit) | count×amount must be within ±5 % of product_quantity, otherwise trust product_quantity and set count=1 | total |
| 4 container name | `categories_tags` + unit + count | yogurt/dessert/skyr/quark and ≤ 500 g → **Becher**; bars (`en:cereal-bars`, `en:chocolate-bars`) → **Riegel**; sodas/beers/energy drinks with ml ≤ 500 and (metal) can → **Dose**; beverages otherwise → **Flasche**; count>1 → name the *single unit* ("1 Becher (125 g)") plus a whole-pack portion | portion rows |
| 5 serving | `serving_size` + `serving_quantity` | regex `(\d+(?:[.,]\d+)?)?\s*(scheibe\|stück\|riegel\|becher\|el\|tl\|portion\|glas\|tasse)n?\s*\(?(\d+(?:[.,]\d+)?)\s*(g\|ml)\)?`, e.g. "1 Scheibe (25 g)" → Scheibe = 25 g; grams per unit = grams ÷ count | **Scheibe**, **Stück**, **EL**, **TL** |
| 6 fallback | none of the above | only an explicit g/ml serving (today's behaviour), else no portion | – |

Failure modes:
- **Multipacks:** "6 x 125 g" is one cup of 125 g, not 750 g. `product_quantity` holds the total, so never call it a "Becher".
- **Drained weight:** "Abtropfgewicht 240 g / 400 g" on canned goods. The nutrition basis is usually the drained product, so prefer the drained value and skip "Dose" as a portion.
- **ml vs g:** Nutrition is per 100 g or per 100 ml (`nutrition_data_per`). Convert ml→g only for drinks with a density close to 1. For oils, syrups and dairy, keep ml-only portions or skip them.
- **Bad data:** totals that look like typos ("1000 kg"), servings bigger than the pack, counts above 48, and EL/TL with no gram value. For EL/TL, add a gram value only from a curated per-category table, never from a guess.
- **Language:** `serving_size` is in German, English or French ("slice", "tranche"). Match all three to the same German label.

## G4 · German names

### 1. BLS 4.0

- **Licence:** CC BY 4.0. The Max Rubner-Institut must be named as publisher [blsdb.de download](https://blsdb.de/download) (search).
- **Citation:** "Max Rubner-Institut (2025): Bundeslebensmittelschlüssel (BLS), Version 4.0" plus the DOI listed in the FAQ [blsdb.de FAQ](https://blsdb.de/faq) (search). I could not confirm DOI `10.25826/Data20251217-134202-0` by search; check it at doi.org. The dataset is also listed on [GovData](https://www.govdata.de/suche/daten/bundeslebensmittelschlussel-bls-version-4-0-deutsche-nahrstoffdatenbank) and [OpenAgrar](https://www.openagrar.de/receive/openagrar_mods_00112643) (search).
- **App use:** The FAQ names research, nutrition counselling, **app development** and the food industry as permitted uses [blsdb.de FAQ](https://blsdb.de/faq) (search).
  - CC BY 4.0 allows commercial redistribution and adaptation if we give attribution, link the licence and say what we changed [CC BY 4.0 legalcode](https://creativecommons.org/licenses/by/4.0/de/legalcode) (search).
  - Serving BLS values through our API is therefore fine. It needs a notice in the app's licences page ("Contains data from the Bundeslebensmittelschlüssel 4.0, Max Rubner-Institut, CC BY 4.0, modified") and should carry no share-alike or ODbL entanglement.
- **Scope:** 7,140 foods and up to 138 nutrients, 418 columns. It has been free since 15/16 Dec 2025 [heise](https://www.heise.de/en/news/Bundeslebensmittelschluessel-4-0-Nutrient-database-now-publicly-available-11123877.html) (search), [BLS 4.0 docs](https://blsdb.de/assets/uploads/BLS_4_0_Dokumentation_DE.pdf) (search).
- **Files:** The first three columns are the BLS code, the **German name and the English name**. There is an English file (`BLS_4_0_Data_2025_EN.xlsx`) and a bilingual nutrient list (`BLS_4_0_Components_DE_EN.xlsx`) [blsdb.de FAQ / docs](https://www.blsdb.de/) (search).

### 2. What a full import adds

- **German names:** a native German name for each of 7,140 foods, against 3 curated BLS foods today.
- **Contents:** The selection was rebuilt for current eating habits, including plant drinks, quinoa and buckwheat. It includes standardised recipes for common dishes [heise](https://heise.de/-11123877) (search). Version 3.02 had about 10,000 entries including dishes and recipes [yumda](https://www.yumda.com/de/lexikon/Bundeslebensmittelschl%C3%BCssel.html) (search).
- **Recipe share:** not published in anything I found.
- **Overlap with SR Legacy:**
  - Unknown. My estimate is that most of BLS's raw and basic foods (produce, meat cuts, dairy, cereals) have an SR counterpart.
  - BLS dishes such as Gulasch, Kartoffelsalat and Brötchen types are mostly German-only, and that is where BLS adds the most.
- **Mapping:** I found no published BLS↔USDA crosswalk.
  - SR is fully indexed with LanguaL, and LanguaL 2017 includes FoodEx2 [LanguaL](https://langual.org/) (search). I found no BLS→FoodEx2 or BLS→LanguaL table.
  - Because BLS ships **English names**, a fuzzy match of BLS EN names to SR descriptions is a cheap substitute.

### 3. Machine translation vs BLS vs curated aliases

- **Cost is trivial:** 7,729 names × ~50 chars ≈ 0.4 M characters. That fits DeepL API Free (500k/month) or costs about $10 on Pro at $25 per 1 M characters [DeepL pricing via G2](https://www.g2.com/products/deepl-api/pricing) (search).
- **Quality is the problem:**
  - USDA names are inverted, comma-separated descriptors. "Potatoes, boiled, cooked without skin, flesh, without salt" becomes "Kartoffeln, gekocht, ohne Schale gekocht, Fruchtfleisch/Fleisch, ohne Salz" (my example, not a test run).
  - German users search "Salzkartoffeln" or "Pellkartoffeln". "flesh" risks becoming "Fleisch". US-only items (Babyfood, Fast foods, brand-like entries) get German names that nobody types.
  - Every name would still need review, so MT saves little.
- **Curated aliases:** Top-query lists such as Quark, Brötchen and Haferflocken give the best precision for the queries that matter. They scale only to a few hundred entries.
- **Recommended path: import BLS 4.0 in full as the German generic layer, and keep curated aliases for the top queries.**
  - Rank BLS first for `lang=de`.
  - Do not machine-translate USDA wholesale.
  - Attach a BLS German name to a USDA food only where an EN-name match was reviewed.
  - Leave unmatched USDA foods English-only, ranked lower in DE search.
- **First step:** Load `BLS_4_0_Data_2025` into a staging schema. Count main groups and the share of dishes. Fuzzy-match the EN names to SR Legacy descriptions and review a 200-row sample to measure precision before writing a migration.

## Recommendation

- **G3:** Count first. Run one DuckDB query on the Parquet file to get the real number of DE/AT/CH products with `nutrition_data_per = 100g` and energy plus the main macros. Then import, with the Pro plan as a requirement: about 0.5 to 3 GB, which does not fit Free's 500 MB.
- **G3:** Ship the tiered portion parser: OFF's normalised `product_quantity` and `serving_quantity` first, then regex on `quantity`/`serving_size`, then container names from categories. Treat multipacks as single units, and skip drained-weight and ml/g-ambiguous portions.
- **G3 (licence):** Treat the OFF slice as an ODbL Derivative Database.
  - Show "(c) Open Food Facts contributors, ODbL" on product screens, the licences page and the App Store listing.
  - Publish a downloadable ODbL dump of our OFF-derived rows, or the parser as an alteration method.
  - Import no images.
  - Have a lawyer confirm the Collective-Database reading for the USDA/BLS rows and whether a query API counts as "Publicly Use".
- **G4:** Import BLS 4.0 in full (7,140 foods, 138 nutrients, CC BY 4.0, app use explicitly allowed) as the primary German generic catalogue, with attribution to the Max Rubner-Institut and a "modified" note.
- **G4:** Do not machine-translate USDA. Bridge BLS↔USDA with reviewed English-name matches, and keep the curated alias list for the top few hundred German queries.
