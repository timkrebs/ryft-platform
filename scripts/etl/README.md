# RYFT Nutrition ETL

Die Pipeline importiert Open Food Facts und USDA FoodData Central in `nutrition.foods` und die zugehörigen Tabellen der Go-API. Ohne `--write` gibt die CLI nur Statistiken aus. Sie verbindet sich dabei mit keiner Datenbank.

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

Der folgende Dry-Run liest die veröffentlichten Quellen. `--limit` begrenzt die Zahl der extrahierten Zeilen je Release nach den SQL-Quellfiltern und vor der Validierung. `--source` akzeptiert `all`, `off`, `usda`, `foundation` und `sr-legacy`. Die drei Pfadargumente erlauben einen vollständig lokalen Testlauf.

```bash
python run_etl.py --source all --limit 10000
python run_etl.py --source all --limit 10000 \
  --off-parquet /tmp/food.parquet \
  --foundation-zip /tmp/foundation.zip \
  --sr-legacy-zip /tmp/sr-legacy.zip
```

Für Schreibläufe die regulären Go-Migrationen bis einschließlich `0045_nutrition_classification_origin.sql` anwenden. Als DB-Admin einen Datenbank-Login für den Job anlegen und ihm `ryft_nutrition_etl` zuweisen. Diese Rolle sieht nur den öffentlichen Katalog und hat keine Rechte auf Mahlzeiten oder CGM. Für temporäre COPY-Tabellen braucht der Login `TEMP` auf der Datenbank. Die Transaktion bleibt jeweils bei einer DB-Session. Für `DATABASE_URL` eine direkte Verbindung oder einen Session-Pooler verwenden und TLS entsprechend der Datenbankkonfiguration erzwingen.

```sql
GRANT ryft_nutrition_etl TO nutrition_etl_login;
```

Mit konfigurierter `DATABASE_URL` den Stichprobenimport und anschließend bei Bedarf den vollständigen Import ausführen.

```bash
python run_etl.py --source all --limit 10000 --write
python run_etl.py --source all --full --write
```

Für Cloud SQL alternativ `INSTANCE_CONNECTION_NAME`, `PGUSER`, `PGDATABASE` und `PGPASSWORD` setzen. Der Client nutzt dann `/cloudsql/$INSTANCE_CONNECTION_NAME`. Auf Cloud Run muss der Job diese Instanz eingebunden haben und sein Konto `roles/cloudsql.client` besitzen. Für Supabase `DATABASE_URL` verwenden. Eine Cloud-SQL-Instanz mit ausschließlich privater IP braucht einen passenden Netzwerkpfad, auch wenn der Client einen lokalen Socket verwendet. [Cloud SQL und Cloud Run](https://docs.cloud.google.com/sql/docs/postgres/connect-instance-cloud-run)

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

```bash
export GCP_PROJECT=my-staging-project GCP_REGION=europe-west1
export ARTIFACT_REPOSITORY=nutrition IMAGE_TAG="$(git rev-parse --short HEAD)-$(date -u +%Y%m%d%H%M%S)"
export JOB_SERVICE_ACCOUNT="nutrition-etl@$GCP_PROJECT.iam.gserviceaccount.com"
export SCHEDULER_SERVICE_ACCOUNT="nutrition-scheduler@$GCP_PROJECT.iam.gserviceaccount.com"
export DATABASE_URL_SECRET=nutrition-db-url:1
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
