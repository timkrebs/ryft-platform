# RYFT Nutrition ETL

Die Pipeline importiert Open Food Facts, USDA FoodData Central und drei kuratierte Datensätze des Bundeslebensmittelschlüssels in den bestehenden Nutrition-Katalog. Ohne `--write` gibt die CLI nur Statistiken aus. Sie verbindet sich dabei mit keiner Datenbank.

## Lokal testen

Im Repository `plattform` ausführen. Die Tests erzeugen kleine Parquet- und ZIP-Fixtures und verwenden für externe API-Antworten lokale HTTP-Mocks.

```bash
cd scripts/etl
python3.11 -m venv /tmp/ryft-nutrition-etl-venv
source /tmp/ryft-nutrition-etl-venv/bin/activate
python -m pip install -r requirements.txt
python -m unittest discover -s tests -v
```

Für den PostgreSQL-Integrationstest `DATABASE_URL` auf eine leere, entbehrliche lokale Datenbank mit Migrationsrechten setzen. Der Test schreibt synthetische Katalogzeilen und wechselt zur ETL-Rolle.

```bash
(cd ../../backend && go run ./cmd/migrate)
RYFT_ETL_TEST_DATABASE_URL="$DATABASE_URL" python -m unittest discover -s tests -v
```

## Import starten

Der folgende Dry-Run liest die veröffentlichten Quellen. `--limit` begrenzt die Zahl der extrahierten Zeilen je Release nach den SQL-Quellfiltern und vor der Validierung. Kuratierte USDA-IDs kommen bei begrenzten Läufen zuerst. `--source` akzeptiert `all`, `off`, `usda`, `foundation`, `sr-legacy` und `bls`. Die drei Pfadargumente erlauben einen vollständig lokalen Testlauf. `--source bls` liest ausschließlich die mitgelieferten Datensätze (die drei Staples und, wenn eingecheckt, `data/bls_4_0.csv`).

```bash
python run_etl.py --source all --limit 10000
python run_etl.py --source all --limit 10000 \
  --off-parquet /tmp/food.parquet \
  --foundation-zip /tmp/foundation.zip \
  --sr-legacy-zip /tmp/sr-legacy.zip
```

Für Schreibläufe die regulären Go-Migrationen bis einschließlich `0059_rank_nutrition_catalog.sql` anwenden (der Import braucht `0055` bis `0058`). Als DB-Admin einen Datenbank-Login für den Job anlegen und ihm `ryft_nutrition_etl` zuweisen. Diese Rolle sieht nur den öffentlichen Katalog und hat keine Rechte auf Mahlzeiten oder CGM. Für temporäre COPY-Tabellen braucht der Login `TEMP` auf der Datenbank. Die Transaktion bleibt jeweils bei einer DB-Session. Für `DATABASE_URL` eine direkte Verbindung oder einen Session-Pooler verwenden und TLS entsprechend der Datenbankkonfiguration erzwingen.

USDA-Haushaltsmaße kommen aus `food_portion.csv` mit dokumentiertem Grammgewicht und stabiler USDA-Portions-ID. Der Import verknüpft `measure_unit.csv` und ignoriert Platzhalter wie `undetermined` in der Anzeige. Open Food Facts liefert Portionsgrößen, wenn auf dem Produkt ein eindeutiger Gramm- oder Milliliterwert steht und der Bezugswert dieselbe Einheit verwendet. Eine Aktualisierung ersetzt nur veraltete Portionen des Python-Imports. Bei einer schon importierten USDA-Portion übernimmt die Aktualisierung ihre bestehende RYFT-ID, wenn der alte Messwert und das alte Label übereinstimmen. Spätere Korrekturen von Label oder Anzeige behalten diese ID. Ändert die Quelle das Gewicht, das Volumen oder die Einheit, bekommt die Portion eine neue ID (sie enthält den Messwert) und die alte wird entfernt; eine gespeicherte Auswahl mit der alten ID wird dann abgelehnt, statt still die neue Menge zu buchen. Pro Lebensmittel ist höchstens eine Portion Standard. Gespeicherte Mahlzeiten behalten ihre Portions- und Nährwert-Snapshots.

**Portionslabels (G1).** Jede nicht kuratierte USDA-Portion bekommt ein sauberes Label statt des Rohtexts (`1 medium (3" dia)`). `transform.py` entfernt Platzhalter wie `undetermined`, streicht Klammern mit imperialen Maßen (`(3" dia)`, `(8 fl oz)`, `(1 oz)`) sowie Zollangaben außerhalb von Klammern (`1/2"`, `dia`) und zerlegt den Rest in Menge, Kopf und Qualifier. Die Tabellen `SIZES`, `UNITS` und `QUALIFIERS` liefern das englische Label, das deutsche Label, die Kurzlabels und die Sortierung (Größen 5 bis 38, Haushaltsmaße ab 40; unbekannte Köpfe behalten `100 + Reihenfolge`). `oz`, `lb` und `fl oz` erscheinen als Gramm bzw. Milliliter. Ist ein Qualifier nicht in der Tabelle, bleibt das deutsche Label ganz weg und die App fällt auf das englische zurück; es gibt nie gemischte Labels. Ein unbekannter Kopf behält den bereinigten englischen Text ohne deutsches Label. Haben zwei Portionen eines Lebensmittels dasselbe Label bei unterschiedlichem Gewicht, hängt der Import ` (<g> g)` an. Das ändert weder die Menge der Portionen noch ihre IDs: Der Roh-Dedupe-Schlüssel und `legacy_label` (der Text, aus dem die ID einer Portion ohne USDA-ID entsteht) bleiben unverändert. `legacy_label` geht jetzt zusätzlich in die Staging-Zeile, damit der Merge handgepflegte Portionen des Go-Importers (Label = Rohtext) weiter als Zwilling erkennt und keine Dubletten anlegt. Ein Standard entsteht nie aus einer Heuristik, nur aus den kuratierten Einträgen.

Der Dry-Run meldet je USDA-Release `portions` (`total`, `relabelled`, `localized_de`, `unmapped_head`, `raw_marker`, erwartet 0) und `staples` (`applied`, `description_mismatch`, `unmatched`). Das Zählen ändert nichts am Geschriebenen.

### Kuratierte Grundnahrungsmittel und Quellen

`staples.v1.json` versioniert RYFT-Anzeigenamen, Suchaliase, Untertitel, Zubereitung und Portionsauswahl. Es führt ungesüßten Apfelsaft als eigenen Eintrag neben dem ganzen Apfel. USDA-Nährwerte bleiben im Originaldatensatz. Eine kuratierte Portionsanzeige wird nur übernommen, wenn die Rohlieferung dieselbe USDA-Portions-ID und dasselbe Gewicht enthält. Die US-Portionsgrößen von Eiern sind als solche gekennzeichnet. Das Auflösen von Quell- und Portions-IDs ändert die bestehenden IDs im Ernährungstagebuch nicht. [USDA FoodData Central, SR Legacy](https://fdc.nal.usda.gov/download-datasets/)

Neue Einträge (Kartoffeln gekocht, Avocado, Tomate, Karotte, Salatgurke, rote Paprika, Zwiebel, Naturjoghurt) tragen `source_description`: Das Grundnahrungsmittel gilt nur, wenn die normalisierte USDA-Beschreibung der Zeile genau diesem Text entspricht. Sonst importiert das Lebensmittel als normales USDA-Lebensmittel und der Dry-Run nennt die FDC-ID unter `description_mismatch`. Ihre Portionen sind nicht an eine USDA-Portions-ID gebunden. Ein Eintrag mit `match` (`key` aus dem bereinigten Label wie `small` oder `medium`, `text` als Präfix des bereinigten Rests, `grams`) gilt nur, wenn genau eine Rohzeile passt. Er übernimmt deren Gewicht und USDA-Portions-ID, also die normale ID, und setzt `portion_key`, Labels, Reihenfolge und Standard. `half_of` leitet daraus eine halbe Portion ab (Avocado: ½ ist der Standard); sie hat keine USDA-ID, ihre ID entsteht aus `portion_key` und Gewicht. Gewichte stehen nie in der JSON-Datei. Was nicht eindeutig passt, erscheint unter `unmatched` und bleibt eine normale Portion.

`staples.v1.json` trägt jetzt `"version": 2` (der Dateiname bleibt). Das ist nötig: Der Merge wertet eine geänderte Prüfsumme bei gleicher `dataset_version` als Konflikt, und die USDA- und BLS-Version enthält `staples<version>`. Nach der Erhöhung schreibt ein Lauf einmal `updated`, ein weiterer identischer Lauf `unchanged`.

Vollmilch 3,5 % (M111300), fettarme Milch 1,5 % (M111200) und Magerquark (M713100) verwenden pro 100 g die ausgewählten Nährwerte aus **Max Rubner-Institut (2025), Bundeslebensmittelschlüssel (BLS), Version 4.0 — Deutsche Nährstoffdatenbank, Karlsruhe**. Der Import normalisiert diese Auswahl in das RYFT-Nährstoffregister. Die Originaldateien sind im Job nicht erforderlich. Quelle unter [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). [Bundeslebensmittelschlüssel und DOI](https://doi.org/10.25826/Data20251217-134202-0)

Das Milchglas misst 200 ml. Die Gramm-Äquivalente für 30, 200 und 300 ml verwenden den Referenzwert 1,030 g/ml für Vollmilch bzw. 1,034 g/ml für teilentrahmte Milch. Das Gewicht einzelner Produkte kann davon abweichen. Die 25-, 150- und 250-g-Einträge für Magerquark sind voreingestellte Gewichte. [BZfE zum 200-ml-Milchglas](https://www.bzfe.de/presse/pressemeldungen-archiv/weniger-milch-mehr-kaese-und-butter) · [FAO/INFOODS Density Database 2.0, S. 10](https://www.fao.org/fileadmin/templates/food_composition/documents/density_DB_v2_0_01.pdf)

### Vollständiger BLS-Import (G4, D296)

Der BLS 4.0 (7.140 Lebensmittel) kommt als geprüfter, eingecheckter Auszug in den Katalog. Der Job lädt nichts herunter, braucht kein DuckDB und keine neue Abhängigkeit.

**Konverter.** `bls_convert.py` ist ein Offline-Werkzeug (nur Standardbibliothek, nicht im Image) und macht aus der offiziellen Arbeitsmappe `data/bls_4_0.csv` und `data/bls_4_0.manifest.json`.

```bash
cd scripts/etl
python3 bls_convert.py BLS_4_0_Daten_2025_DE.xlsx [--sheet NAME] [--unit CODE=Einheit ...] [--out-dir data]
```

Der Konverter liest das Blatt zeilenweise (`iterparse`, jede Zeile wird danach freigegeben), der Speicherbedarf wächst also nicht mit der Tabelle. Er behandelt Shared Strings, Inline-Strings, Formelstrings, Zahlen, dünn besetzte Zeilen (über die Zellreferenz) und deutsche Dezimalkommas. Die BLS-Marker gelten genau so, wie die Datei sie schreibt: `TR` (Spuren), `<LOD`, `<LOQ` und `<LOD or <LOQ` (unter der Nachweis- oder Bestimmungsgrenze) werden 0, `-` (keine Daten) bleibt unbekannt; das Manifest zählt sie je Komponente (`markers`). Leere Zellen und `-` bleiben leer, auch in einer Pflichtkomponente; die ETL lehnt eine solche Zeile später ab (`missing_or_invalid_macro`). Andere Platzhalter wie `n.b.` und negative Werte sind unbekannt und brechen in einer Pflichtkomponente den Lauf ab. Das Archiv ist begrenzt (Mitglieder und Gesamtgröße), doppelte Mitglieder werden abgelehnt. Die Kopfzeile ist die erste Zeile (in den ersten 100), in der mindestens drei der Pflichtcodes (`ENERCC`, `ENERCJ`, `PROT625`, `FAT`, `CHO`) als erstes Wort einer Zelle stehen. Die Tabelle `BLS_COMPONENTS` ordnet je RYFT-Nährstoffcode die BLS-Codes zu (bevorzugter zuerst). `CHO` ist der verfügbare Kohlenhydratanteil und wird `carbohydrates`, nie `carbohydrates_total`. Die Einheit liest der Konverter aus der Kopfzelle (`[mg/100g]`, `[µg/100g]`, `[kcal/100g]`, `[kJ/100g]`, auch `[g]`) und rechnet auf die Registereinheit um. Nennt die Kopfzelle keine (oder zwei) Einheiten, bricht er ab, bis `--unit` sie vorgibt (BLS- oder RYFT-Code, z. B. `--unit SE=ug`). Er bricht ebenfalls ab, wenn eine Pflichtkomponente fehlt (energy kcal oder kJ, protein, fat, carbohydrates), ein Code in mehreren Kopfzellen steht, ein BLS-Code doppelt vorkommt oder kein Blatt eine Kopfzeile hat. Die Meldung nennt dann die gefundenen Kopfzellen, und es entsteht keine Ausgabe. Zeilen, deren Code nicht auf `^[A-Z][0-9][0-9A-Z]{2}[0-9]{3}$` passt (BLS 4.0 hat Codes wie `M5B1600`; dasselbe Muster prüfen `transform.BLS_ID` und die Constraint `nutrition_bls_attribution`, Migration 0062) (Fußzeilen, Zwischenüberschriften), werden übersprungen und gezählt. Ein zweiter Lauf erzeugt byte-identische Dateien. Der Konverter braucht Python 3.11 oder neuer und meldet sich sonst mit einer klaren Fehlermeldung.

Zellen eines zugeordneten Nährstoffs, die keine Zahl sind (`-`, `n.b.`, Text), zählt der Konverter je Komponente (`unparsed`) und nennt bis zu fünf Beispielwerte (`unparsed_samples`) im Manifest. Hat eine Pflichtkomponente (protein, fat, carbohydrates und jede zugeordnete Energie) auch nur eine solche Zelle, bricht er ab. Ein Textfeld wie `1.234` oder `1.234,5`, das wie ein deutsches Tausendertrennzeichen aussieht, bricht ab, sobald in den zugeordneten Spalten Dezimalkomma-Text vorkommt (`1.234,5` immer); echte Zahlenzellen sind nie betroffen. Die Spalten 1 bis 3 (Code, deutscher, englischer Name) prüft er lose an ihren Kopfzellen (Code: `code|schlüssel|sbls|bls`; deutsch: `bezeichnung|name|deutsch`, aber nicht `engl`; englisch: `engl|english|food name|name en|(en)`). Weichen sie ab, bricht er mit den drei Kopfzellen ab, bis `--name-columns CODE,DE,EN` (1-basiert) die Spalten vorgibt; die Option schaltet die Prüfung ab. `*.xlsx` unter `scripts/etl` ist git-ignoriert: Die Arbeitsmappe wird nie eingecheckt.

Das Manifest trägt `csv_sha256`. CSV und Manifest werden als Paar geschrieben: erst beide Temp-Dateien, dann ersetzt der Konverter die CSV und zuletzt das Manifest. Bricht ein Lauf dazwischen ab, passt die CSV nicht zum Manifest, und der Import verweigert das Paar.

Die CSV hat die Spalten `bls_code,name_de,name_en` gefolgt von den 36 Registercodes in dieser Reihenfolge: `energy_kcal, energy_kj, protein, fat, carbohydrates, fiber, total_sugar, saturated_fat, monounsaturated_fat, polyunsaturated_fat, trans_fat, epa, dha, sodium, potassium, calcium, magnesium, iron, zinc, phosphorus, selenium, vitamin_a, vitamin_c, vitamin_d, vitamin_e, vitamin_k, vitamin_b1, vitamin_b2, vitamin_b3, vitamin_b6, vitamin_b12, folate, caffeine, alcohol, water, cholesterol`. Werte gelten je 100 g in den Registereinheiten, ein leeres Feld ist unbekannt, die Zeilen sind nach BLS-Code sortiert. Das Manifest hält je Nährstoff BLS-Code, Kopfzelle, Spalte, Einheit (und ob sie aus Kopfzelle oder `--unit` kam), Faktor und Zahl der Werte fest, dazu Quelldatei, SHA-256, Blatt, Kopfzeile, geschriebene und übersprungene Zeilen und die Konverterversion. Der Hinweis nach CC BY 4.0 steht in `data/README.md`.

**Kuratierung.** `bls_curation.v1.json` (`{"version": 1, "synonyms": [...], "twins": {...}}`) wird beim Laden geprüft und gehört zum Image.

- `synonyms`: Gruppen gleichwertiger Schreibweisen (Möhre, Karotte, Mohrrübe, Rüebli). Jedes Mitglied, das als ganzes Wort (Unicode-Wortgrenzen, Groß- und Kleinschreibung beachtet) im deutschen BLS-Namen steht, wird durch jedes andere Mitglied seiner Gruppe ersetzt. Das ergibt je einen deutschen Alias (`alias = true`), also Ersetzung und keine Übersetzung. `Lauchzwiebel`, `Sauerrahm` und das Adjektiv `orange` treffen nie. Doppelte Aliase und solche, deren normalisierte Form schon ein Name des Lebensmittels ist, entfallen. Es gibt höchstens 6 pro Lebensmittel, in der Reihenfolge der Datei. Kuratierte Grundnahrungsmittel behalten ihre eigenen Aliase.
- `twins`: `{"<BLS-Code>": {"staple": "usda:<fdc_id>", "name_de": "<erwarteter deutscher BLS-Name>"}}` für BLS-Lebensmittel, die dasselbe Lebensmittel wie ein kuratiertes USDA-Grundnahrungsmittel sind. Ein Zwilling, dessen normalisierter Name noch passt, wird nicht importiert (`skipped_twin`), das Grundnahrungsmittel und seine Portionen bedienen ihn. Passt der Name nicht mehr, importiert die Zeile normal und steht unter `twin_mismatch`. Geprüft mit dem Datenstand vom 2026-11-27: 20 Zwillinge, je der rohe oder genau passend zubereitete Zustand des Grundnahrungsmittels (Liste und Begründung in D296). `twin_candidates` schlägt vor, wenn der erste Kommaabschnitt, der ganze Name, der Name ohne Zustandswörter (`roh`, `gekocht`, …) oder dieser ohne Leerzeichen einem Namen oder Alias eines Grundnahrungsmittels gleicht. Ein Zwilling muss auf ein kuratiertes USDA-Grundnahrungsmittel zeigen und darf kein kuratierter BLS-Code sein.

**Import.** `--source bls` und `--source all` lesen `data/bls_4_0.csv` neben dem Skript, wenn die Datei existiert (`--bls-csv PFAD` wählt eine andere, `--no-bls-csv` erzwingt den Weg ohne CSV). Vor dem Lauf prüft der Import, dass das Manifest neben der CSV (`bls_4_0.manifest.json`) existiert und sein `csv_sha256` zur CSV passt, und liest die ganze CSV (Spalten, Feldzahl, BLS-Codes, doppelte Codes, Zahlen) vor dem ersten Batch. Eine fehlerhafte Datei stoppt den Lauf, bevor etwas verarbeitet oder geschrieben wird. Die Version ist `py-etl4:bls_4_0:staples<Version>:<Digest>`. Der Digest deckt CSV, Kuratierungsdatei, `staples.v1.json` und die Konstante `BLS_TRANSFORM_VERSION` in `transform.py` ab (`bls1`; erhöhen, wenn `bls_row_food`, die Formen, die Hauptgruppen, die Alias-Logik oder die Kategorien für BLS sich ändern). Eine geänderte Eingabe gibt daher `updated` und nie `conflict`, ein identischer Lauf `unchanged`. Quelle `bls`, externe ID ist der BLS-Code, Lizenz `CC-BY-4.0`, Markt `DE`. Jede Zeile läuft durch dieselbe Validierung wie USDA (Alkohol ist die Ausnahme der Atwater-Prüfung). Der Name ist der deutsche, der englische kommt als zweiter Name dazu. Fehlt `energy_kcal`, wird es aus kJ abgeleitet (`bls:kcal_from_kj`). Die Kategorie ist `classify(Name, [BLS-Hauptgruppe nach dem ersten Buchstaben des Codes], provider="bls")`, nur für die Suche, es gibt kein `ryft_nutrition`-Profil. `food_form` kommt aus ganzen deutschen Zustandswörtern (roh, gekocht, gegart, gebraten, gebacken, getrocknet, tiefgefroren/tiefgekühlt, das erste Treffer-Wort in dieser Reihenfolge), sonst bleibt es leer. Es gibt keine Portionen (nur 100 g und die g/ml-Eingabe der App), keinen `canonical_group_key` und kein Grundnahrungsmittel-Flag. Die drei kuratierten Staples (M111300, M111200, M713100) behalten Namen, Aliase, Portionen, Gruppe, Form, Untertitel und Dichte exakt, ihre Nährwerte stammen aber aus der CSV-Zeile.

**Ohne CSV** (solange keine Datei eingecheckt ist) importiert `--source bls` die drei Staples wie bisher, mit der Version `py-etl4:bls_4_0:staples2` (also `unchanged`) und `"bls_full": false`. `data/README.md` liegt immer im Image, damit `COPY data/ data/` funktioniert.

**Dry-Run.** Die Zeile `bls` enthält zusätzlich zu den üblichen Zählern `bls_full` (`true`), `aliases_added`, `skipped_twin`, `twin_mismatch` (BLS-Code → erwarteter und gefundener Name), `twin_candidates` (BLS-Code → Name und `usda:<fdc_id>`, wenn das erste Komma-Segment des deutschen Namens normalisiert einem deutschen Namen oder Alias eines USDA-Staples entspricht, nur zur Prüfung), `stale_twins` (Zwillingscodes der Kuratierung, die in der CSV vorkommen; mit Datenbankverbindung (`--write`) zusätzlich `imported`, ob schon eine `bls`-Quelle für den Code existiert, sonst `null`; nur lesend: Ein nach einem Schreiblauf ergänzter Zwilling lässt den früheren Import stehen und wird von Hand zurückgezogen), `staple_nutrient_drift` (Staple-Code → Nährstoff → `[kuratiert, CSV]`, wenn beide um mehr als 1 % abweichen oder die CSV den Wert nicht hat) und `staples_missing` (kuratierte BLS-Codes, die in der CSV nicht vorkamen, bei `--limit` erwartbar). Übersprungene Zwillinge stehen in `counts.skipped` und zählen weder als akzeptiert noch als abgelehnt.

**Tests.** `tests/test_bls.py` baut die Arbeitsmappen zur Laufzeit mit `zipfile`. Er prüft Konverter, Fail-closed-Fälle, Umrechnung und Byte-Identität, den Transform (Aliase, Formen, Zwillinge, Staples), die CLI und, mit `RYFT_ETL_TEST_DATABASE_URL`, den Import samt Wiederholung gegen PostgreSQL. Ein Speichertest erzeugt ein Blatt in Originalgröße (7.140 Zeilen, 418 Spalten) und verlangt, dass der Konverter in einem Unterprozess unter 512 MiB bleibt.

**Betriebsschritte (D296).**

1. `BLS_4_0_Daten_2025_DE.xlsx` von https://blsdb.de/download nach `scripts/etl/` laden. `*.xlsx` ist dort git-ignoriert.
2. In `scripts/etl` mit Python 3.11+ `python3 bls_convert.py BLS_4_0_Daten_2025_DE.xlsx` ausführen. Bricht der Konverter ab, nennt er die gefundenen Kopfzellen. Die Abhilfe ist dann `--unit` oder `--name-columns`, nie ein Eingriff in die CSV.
3. Lokal `python3 run_etl.py --source bls` ausführen, ohne Datenbank. Dabei prüfen:
   - `twin_candidates`: die geprüften Paare mit dem erwarteten deutschen Namen in `bls_curation.v1.json` → `twins` eintragen, **vor dem ersten Schreiblauf**.
   - `staple_nutrient_drift` und `staples_missing`.
   - die Ablehnungen.
   - `unparsed` im Manifest.
4. CSV, Manifest und `twins` in einem Commit per PR einchecken.
5. Nach dem Merge das Image veröffentlichen: `gh workflow run deploy.yml --ref main -f publish_nutrition_etl=true`.
6. Den Digest in der lokalen `infra/gcp/terraform.tfvars` setzen (nicht in HCP). Dann `terraform -chdir=infra/gcp plan -out=bls-full.tfplan` und `apply bls-full.tfplan` ausführen.
7. Dry-Run: `gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --args=--source,bls,--full --wait`.
8. Schreiblauf: dasselbe mit `--args=--source,bls,--full,--write`. Ein zweiter Lauf meldet `unchanged`.

Ein nachträglich eingetragener Zwilling bleibt aktiv (`stale_twins`), bis er von Hand über `status` stillgelegt wird. Der Merge löscht oder deaktiviert nie.

```sql
GRANT ryft_nutrition_etl TO nutrition_etl_login;
```

Mit konfigurierter `DATABASE_URL` den Stichprobenimport und anschließend bei Bedarf den vollständigen Import ausführen.

```bash
python run_etl.py --source all --limit 10000 --write
python run_etl.py --source all --full --write
```

Für Cloud SQL alternativ `INSTANCE_CONNECTION_NAME`, `PGUSER`, `PGDATABASE` und `PGPASSWORD` setzen. Der Client nutzt dann `/cloudsql/$INSTANCE_CONNECTION_NAME`. Auf Cloud Run muss der Job diese Instanz eingebunden haben und sein Konto `roles/cloudsql.client` besitzen. Eine Cloud-SQL-Instanz mit ausschließlich privater IP braucht einen passenden Netzwerkpfad, auch wenn der Client einen lokalen Socket verwendet. [Cloud SQL und Cloud Run](https://docs.cloud.google.com/sql/docs/postgres/connect-instance-cloud-run)

Für Supabase auf Cloud Run muss `DATABASE_URL` den freigegebenen Session-Pooler, Port `5432`, Datenbank `postgres`, den Login mit Projekt-Suffix und `sslmode=verify-full` verwenden. Das Image enthält die von der Go-API verwendete Supabase-CA. Falls der DSN `sslrootcert` enthält, muss der Wert `/etc/ryft/supabase-ca.crt` sein. Prüfe den geheimen DSN und das konkrete Image vor der Freigabe. [Produktions-Preflight](../../infra/gcp/README.md#nutrition-on-cloud-run)

## Verarbeitung und Wiederholung

- DuckDB liest den offiziellen OFF-Parquet-Export über HTTP-Range-Requests von einer festen Revision. Es filtert DACH- und EU-Zeilen und projiziert die benötigten Spalten. Der Import verwirft ungültige GTIN-Prüfziffern und behält führende Nullen. Das Image enthält `httpfs` bereits. DuckDB bricht ab, wenn der Server keine Teilabfragen unterstützt. [Offizieller OFF-Datensatz](https://huggingface.co/datasets/openfoodfacts/product-database)
- USDA Foundation Foods und SR Legacy werden anhand der USDA-Downloadseite aufgelöst. Fehlen Pflicht-CSV-Dateien, lädt die Pipeline das aktuelle Archiv einmal über eine frische URL und versucht bei erneut fehlenden Dateien die vorherige offizielle Ausgabe. Das Protokoll nennt die tatsächlich verarbeitete Ausgabe. DuckDB filtert auf den gewählten USDA-Datentyp, bevor das Zeilenlimit greift, und verknüpft Lebensmittel mit den Nährwerten, Einheiten und Kategorien. Fehlt in einer älteren Ausgabe die Kategorietabelle, greift der Klassifikator auf den Namen zurück. ZIP und CSVs liegen pro Release ausschließlich in einem temporären Verzeichnis unter `/tmp`, das die CLI beim Verlassen entfernt. Der entpackte CSV-Umfang ist auf 192 MiB je Release begrenzt. [USDA-Releases](https://fdc.nal.usda.gov/download-datasets/)
- Die Validierung verlangt einen Namen und vollständige Makros. Für belegbar volumetrisch deklarierte OFF-Getränke bleibt die Bezugsmenge bei 100 mL. Der Import berechnet Ethanolgramm aus ABV mit 0,789 g/mL Ethanol. Angaben ohne eindeutige Bezugsmenge werden verworfen. Massensumme, Energiedeckel und Atwater-Abweichung werden geprüft. Bekannter Alkohol und ein Ballaststoffanteil ab 15 g je Bezugsmenge können die Atwater-Prüfung mit Warnung passieren. Natrium und Koffein werden in Milligramm gespeichert. EPA, DHA, `omega_3` und zugesetzter Zucker verwenden die registrierte Einheit Gramm. Fehlende Nährwerte bleiben unbekannt. [OFF-Nährwertschema](https://openfoodfacts.github.io/documentation/docs/Product-Opener/schemas/schemas/product_nutrition/)
- Die AHEI-Kategorie wird pro Quelle in `nutrition.classifications` unter `ahei_category` gespeichert. Für geeignete einfache USDA-Grundnahrungsmittel und klar erkennbare Getränke ergänzt der Import `ryft_nutrition`-Werte für die Scoring-Engine. Die Anteile unbekannter Mischgerichte bleiben offen. Manuelle Klassifikationen behalten Vorrang.
- COPY schreibt in transaktionslokale, unprotokollierte PostgreSQL-TEMP-Tabellen. Der Merge verwaltet die Quellidentität über `(provider,external_id)`, behält bestehende Lebensmittel-UUIDs und bewahrt Mahlzeiten-Snapshots. Normalisierte Barcodes stehen für die Suche zur Verfügung. Mehrdeutige Produkte werden anhand der jeweiligen Quelle unterschieden. Ein Abbruch lässt abgeschlossene Batches stehen. Der erneute Lauf überspringt unveränderte Daten. Je Release meldet die CLI akzeptierte, abgelehnte, eingefügte, aktualisierte und unveränderte Zeilen sowie Konflikte, Dubletten und die Kategorieverteilung. Die Anzahl durch SQL-Quellfilter ausgeschlossener Zeilen ist in den Ablehnungszahlen nicht enthalten.
- Die Quellen behalten ihren Lizenznachweis. Für die Weitergabe des OFF-Bestands gelten die OFF-Lizenzanforderungen. USDA FDC veröffentlicht seine Daten unter CC0. [OFF-Lizenz](https://world.openfoodfacts.org/terms-of-use) · [USDA-Lizenz](https://fdc.nal.usda.gov/data-documentation.html)

## Optionale Embeddings

`embeddings.sql` als Schema-Admin nach den Go-Migrationen anwenden. Die Datenbank muss die pgvector-Erweiterung mit HNSW bereitstellen. Die Tabelle speichert 768-dimensionale Vektoren im Cosine-Index. Der Worker verarbeitet öffentliche, aktive Katalogeinträge und sendet den Namen und gegebenenfalls die Marke an die Embedding-API. Geänderte Namen oder Marken werden erneut verarbeitet. Unterbrochene Läufe setzen bei den fehlenden oder veralteten Vektoren fort. Die Go-API erhält Leserechte für aktuelle Vektoren. Eine Vektorsuche im Go-Endpunkt ist separat zu integrieren. [pgvector](https://github.com/pgvector/pgvector) · [Embedding-Modell](https://developers.openai.com/api/docs/models/text-embedding-3-small)

```bash
# DATABASE_URL vor dem DDL-Aufruf auf die gewünschte Datenbank mit Schema-Admin-Rechten setzen.
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f embeddings.sql
# DATABASE_URL danach auf den ETL-Login setzen.
python generate_embeddings.py
# OPENAI_API_KEY vor dem folgenden Aufruf sicher in der Shell-Umgebung setzen.
python generate_embeddings.py --limit 1000 --batch-size 500 --write
```

Der erste Python-Aufruf zählt ausstehende Einträge. Der zweite darf kostenpflichtige API-Anfragen senden. Mit `--all --write` wird der gesamte Rückstand verarbeitet. Der monatliche Cloud-Job führt den Worker nicht aus.

## Cloud Run Job

Für `ryftlab-prod` gilt die [Terraform-Rollout-Anleitung](../../infra/gcp/README.md#nutrition-on-cloud-run). Der folgende Standalone-Weg ist für separate Testprojekte vorgesehen.

Artifact Registry, Cloud Build, Cloud Run, Cloud Scheduler und Secret Manager müssen im Zielprojekt eingerichtet sein. Für den Socket-Pfad kommt die Cloud SQL Admin API hinzu. Das Cloud-Build-Konto benötigt Schreibrechte im Artifact-Registry-Repository. Das Jobkonto benötigt Zugriff auf das konfigurierte Secret. Das Deploy-Konto muss die beiden Service Accounts verwenden sowie den Job und seinen Scheduler-Eintrag verwalten können. Das Skript weist dem Scheduler-Konto `roles/run.invoker` am Job zu. [Cloud Run Jobs zeitgesteuert starten](https://docs.cloud.google.com/run/docs/execute/jobs-on-schedule)

Für Supabase `SUPABASE_POOLER_HOST` und `SUPABASE_PROJECT_REF` aus dem freigegebenen Testprojekt exportieren. Der Build liest die öffentliche CA aus `backend/certs/supabase-ca.crt`.

```bash
export GCP_PROJECT=my-staging-project GCP_REGION=europe-west1
export ARTIFACT_REPOSITORY=nutrition IMAGE_TAG="$(git rev-parse --short HEAD)-$(date -u +%Y%m%d%H%M%S)"
export JOB_SERVICE_ACCOUNT="nutrition-etl@$GCP_PROJECT.iam.gserviceaccount.com"
export SCHEDULER_SERVICE_ACCOUNT="nutrition-scheduler@$GCP_PROJECT.iam.gserviceaccount.com"
export DATABASE_URL_SECRET=nutrition-db-url:1
: "${SUPABASE_POOLER_HOST:?Test-Pooler exportieren}" "${SUPABASE_PROJECT_REF:?Testprojekt-Ref exportieren}"
bash deploy_job.sh --plan
```

Für Cloud SQL anstelle von `DATABASE_URL_SECRET` folgenden Block verwenden.

```bash
unset DATABASE_URL_SECRET
export INSTANCE_CONNECTION_NAME="$GCP_PROJECT:$GCP_REGION:my-instance"
export PGUSER=nutrition_etl_login PGDATABASE=ryft
export PGPASSWORD_SECRET=nutrition-db-password:1
bash deploy_job.sh --plan
```

Nach Prüfung von Zielprojekt, Datenbank und Secret-Version mit `bash deploy_job.sh --apply` bereitstellen. Das Skript konfiguriert `nutrition-etl-job` mit zwei vCPUs, 4 GiB RAM, einem Task, einem Retry und 1800 Sekunden Timeout je Versuch. Cloud Scheduler startet `--source all --full --write` standardmäßig am ersten Tag jedes Monats um 04:00 UTC. `SCHEDULE`, `SCHEDULE_TIME_ZONE` und `SCHEDULER_REGION` sind optional. `--plan` gibt die Kommandos aus, ohne GCP zu kontaktieren.

Der Job benötigt kein persistentes Volume. Der DuckDB-Speicherpuffer ist standardmäßig auf 768 MB begrenzt. Python-Prozess, HTTP-Puffer, `/tmp` und DuckDB-Spills benötigen zusätzlichen Container-Speicher. Ein vollständiger Import kann erhebliche Datenbank- und Netzwerkressourcen belegen. Cloud Build, Registry, Scheduler, Job und Datenbank können Kosten verursachen. Dauer und Ressourcenbedarf eines vollständigen Laufs sollten in Staging gemessen werden. [Cloud Run Preise](https://cloud.google.com/run/pricing) · [Cloud Scheduler Preise](https://cloud.google.com/scheduler/pricing)
