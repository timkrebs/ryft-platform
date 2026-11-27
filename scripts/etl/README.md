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

Der folgende Dry-Run liest die veröffentlichten Quellen. `--limit` begrenzt die Zahl der extrahierten Zeilen je Release nach den SQL-Quellfiltern und vor der Validierung. Kuratierte USDA-IDs kommen bei begrenzten Läufen zuerst. `--source` akzeptiert `all`, `off`, `usda`, `foundation`, `sr-legacy` und `bls`. Die drei Pfadargumente erlauben einen vollständig lokalen Testlauf. `--source bls` liest ausschließlich die mitgelieferten Datensätze.

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
