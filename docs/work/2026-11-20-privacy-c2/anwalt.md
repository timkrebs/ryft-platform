# RYFT: Datenschutzerklärung 2026-11-20 zur Prüfung (C2)

Stand 2026-11-20, geprüft gegen das Plattform-Repository, `main` (`1832748`) und die App (Arbeitskopie `ryft/app`,
Abgleich mit einer `main`-Kopie vom 2026-11-17). **Keine Rechtsberatung:** Rechtsgrundlagen und Bewertungen in
diesem Paket sind als *Vorschlag* markiert; Tatsachen nennen ihre Quelle (Datei oder Entscheidung `D<n>` in
`docs/DECISIONS.md`; die Belegliste je Satz steht in `facts.md`).

Im Paket:
- `privacy-2026-11-20.mdx`: der neue Text (Englisch, die Fassung, die veröffentlicht würde);
- `policy-de.md`: eine deutsche Lesefassung davon (nicht veröffentlicht);
- `diff.md`: was sich gegenüber der geltenden Fassung 2026-10-26 ändert;
- dieses Dokument: Überblick, Auftragsverarbeiter, Rechtsgrundlagen (Vorschlag), Speicherdauer, **Fragen**.

---

## 1. Überblick (eine Seite)

**Was RYFT ist.** Eine iPhone-App, die mit Einwilligung Gesundheitsdaten aus Apple Health liest (Herzfrequenz,
HRV, Ruhepuls, Atemfrequenz, SpO₂, VO₂max, aktive Energie, Schritte, Schlafphasen, Workouts), sie an die
RYFT-Server sendet und daraus Erholung, Belastung, Schlaf und Bereitschaft berechnet. Dazu kommen Eingaben der
Nutzer (Krafttraining, Journal mit z. B. Alkohol, Koffein, Cannabis, Krankheit), Push-Mitteilungen und Feedback.

**Wer sie nutzt.** Eine kostenlose Beta, nur über TestFlight an eingeladene Tester; es gibt keine öffentliche
Anmeldung und keine Einladungscodes im Umlauf (D241). Bisher ist der Betreiber selbst der einzige Nutzer;
fünf bis zehn Tester aus dem Bekanntenkreis sind geplant (D239). Keine Werbung, kein Verkauf von Daten, kein
Entgelt.

**Wer verantwortlich ist.** Eine Privatperson, die ihren Namen, ihre Wohnanschrift, private E-Mail und Telefon
nicht veröffentlichen möchte (LEGAL_CHECKLIST Abschnitt 0). Der Text trägt dafür Marker `TODO(user)`; siehe
Frage 1. Kontakt ist die Rollenadresse `support@ryftlab.de`.

**Wie es technisch läuft (seit 2026-11-19).** Die Dienste laufen auf Google Cloud in Belgien (europe-west1,
D216, D217), die Datenbank und die Anmeldung bei Supabase in Frankfurt, die Website ryftlab.de auf Firebase
Hosting (D233). AWS, der Kubernetes-Cluster, Vault, RabbitMQ, Grafana Cloud und das Website-Image gibt es nicht
mehr (D236, D246). E-Mail läuft über Microsoft 365, gebucht über GoDaddy (D214, D239).

**Was neu ist und noch nicht live:**
- **App Attest** (Apple): der Server ist ausgeliefert, aber aus (`APP_ATTEST=off`, Antwort 501, D247); die App
  nutzt es erst in einer kommenden Version. Es darf erst eingeschaltet werden, wenn dieser Text gilt (D245).
- **PostHog** (Produktanalyse, opt-in): der Server liefert das Ziel aus (D231), der Schalter steht auf 0 %, und
  die App hat noch **keinen** Analyse-Code (weder in der Arbeitskopie noch in `main`). Vor diesem Text darf
  niemand freigeschaltet werden (`docs/OPERATIONS.md` → Feature flags, Analytics).

**Was nach der Prüfung bleibt:** die Marker füllen, die Datei nach
`web/content/legal/en/privacy/2026-11-20.mdx` verschieben, `status: published` und `effective` setzen,
`privacy-versions.json` ergänzen und `PRIVACY_POLICY_VERSION` in `deploy/run/config.env` und
`docker-compose.yml` ändern, alles in einem PR. Danach muss jeder Nutzer neu einwilligen.

## 2. Empfänger und Auftragsverarbeiter

| Wer | Welche Daten | Zweck | Wo | Drittland, Grundlage (laut Anbieter) | AVV / DPA |
| --- | --- | --- | --- | --- | --- |
| **Google Cloud** (Google; Cloud Run, Pub/Sub, Scheduler, Secret Manager, Logging, Monitoring, DNS, Load Balancer mit Cloud Armor) | alles, was die Dienste verarbeiten (im Durchgang); Logs mit Konto-ID, Pfad, Status (14 Tage); bei blockierten/markierten Anfragen und Serverfehlern IP, URL, User-Agent (14 Tage); Ereignisse mit Konto-ID und Tagen (7 bzw. 14 Tage) | Betrieb, Sicherheit, Fehlersuche | Belgien (europe-west1); Load Balancer und Cloud Armor im globalen Google-Netz | USA möglich: Google LLC ist DPF-zertifiziert, der CDPA enthält SCC *(Suche)* | Cloud Data Processing Addendum: **Annahme in der Konsole prüfen** (Frage 13) |
| **Firebase Hosting** (Google, Projekt `ryftlab-site`) | IP-Adresse der Besucher zur Auslieferung; kein Zugriffsprotokoll von RYFT | Website ausliefern | globales Google-Netz | wie oben | Firebase-Datenverarbeitungsbedingungen: **offen** |
| **Supabase** | Konto, Gesundheitsdaten, alles in §3; eigene Logs mit IP | Datenbank, Anmeldung, Anmelde-Mails auslösen | Frankfurt (AWS eu-central-1) | Zugriff von außerhalb der EU: SCC (Modul 2) *(Suche)* | DPA mit Supabase Pte. Ltd., Singapur, akzeptiert (Checkliste 0a, S1) |
| **Apple** | Sign in with Apple; Push-Token und Mitteilungstext (kann Werte enthalten, z. B. „Recovery 82 %“); App Attest (Gerät ↔ Apple); TestFlight und dessen Feedback | Anmeldung, Mitteilungen, Geräteprüfung, Beta-Verteilung | Apple | Apple-Bedingungen | keine AVV; Rolle von Apple: Frage 9 |
| **PostHog** (EU Cloud) | nur bei Opt-in: feste Ereignisliste, pseudonyme ID, keine Gesundheitsdaten, IP verworfen | Produktanalyse | Frankfurt (AWS eu-central-1) *(Suche)* | PostHog, Inc. (USA); DPF-Status **nicht gefunden** *(Suche)* | DPA über posthog.com/dpa: **offen** |
| **Microsoft** (Microsoft 365, Exchange Online, über GoDaddy) | Mails an support@; Anmelde-Mails (Adresse, Einmal-Link, Zeit) als no-reply@, mit Kopie in „Gesendete Elemente“ | Support, Anmelde-Mails | **offen** (Datenstandort des Mandanten) | EU Data Boundary, DPF *(Suche)* | Microsoft DPA, ob über GoDaddy wirksam: **offen** (Frage 12) |
| **GoDaddy** | keine Daten aus der App (Registrar; DMARC-Berichte ohne Nutzerdaten); als Wiederverkäufer des Microsoft-Abos möglicherweise Zugang zum Mandanten: **offen** (Frage 12) | Domain, Mail-Abo | – | – | – |

Keine Empfänger von Nutzerdaten: GitHub (Code, CI, Images), HCP Terraform (Infrastruktur-Zustand, keine
Nutzerdaten, D231/`secrets.tf`), die Uptime-Checks von Google (rufen nur öffentliche Adressen ab).

Quellen: `infra/gcp/*.tf` (Region `variables.tf`, Logs `logging.tf`, LB `edge.tf`, Alarme `alerts.tf`,
Pub/Sub `pubsub.tf`), `deploy/run/*`, D214, D217, D218, D222, D231, D233, D236, D239, D242, D244, D245, D247.
Anbieterangaben mit *(Suche)* stammen aus Suchergebnissen, nicht aus gelesenen Vertragstexten.

## 3. Rechtsgrundlagen (*Vorschlag*)

| Verarbeitung | Vorschlag | Anmerkung |
| --- | --- | --- |
| Gesundheitsdaten aus Apple Health, gesundheitsbezogene Eingaben (Journal), berechnete Werte | *Vorschlag:* ausdrückliche Einwilligung, Art. 9 Abs. 2 lit. a DSGVO | Einwilligungsbildschirm vor dem ersten Upload, versioniert; ohne sie keine Uploads (403 `consent_required`, D78) |
| Konto, Einstellungen, Training, Geräte, Push | *Vorschlag:* Art. 6 Abs. 1 lit. b DSGVO | Mitteilungen nur nach iOS-Freigabe; Werte im Mitteilungstext: Frage 8 |
| App Attest | *Vorschlag:* berechtigtes Interesse, Art. 6 Abs. 1 lit. f DSGVO; für Schlüssel auf dem Gerät § 25 Abs. 2 Nr. 2 TDDDG? | Frage 7 |
| Produktanalyse (PostHog) | *Vorschlag:* Einwilligung, Art. 6 Abs. 1 lit. a DSGVO und § 25 Abs. 1 TDDDG | Opt-in, standardmäßig aus, widerrufbar |
| Logs, Cloud Armor, Ratenbegrenzung | *Vorschlag:* Art. 6 Abs. 1 lit. f DSGVO | IP nur bei blockierten/markierten Anfragen und Serverfehlern gespeichert, 14 Tage (D242) |
| Feedback, Support-Mails | *Vorschlag:* Art. 6 Abs. 1 lit. b und f DSGVO | |
| Einladung (gebundene Adresse), Anmelde-Mails | *Vorschlag:* Art. 6 Abs. 1 lit. b DSGVO (vorvertragliche Maßnahme) | #129 |
| Einwilligungsnachweis | *Vorschlag:* Art. 6 Abs. 1 lit. c mit Art. 7 Abs. 1 DSGVO | wie bisher |
| Website | *Vorschlag:* Art. 6 Abs. 1 lit. f DSGVO | keine Cookies, kein Speicher im Browser (außer der Admin-Seite des Betreibers); *Vorschlag:* § 25 TDDDG entfällt (bisher für die Hell/Dunkel-Wahl genannt) |
| Admin-Zugriff des Betreibers | *Vorschlag:* je nach Zweck lit. b/f; Art. 9: von der Einwilligung umfasst? | Frage 10 |

## 4. Speicherdauer

| Daten | Dauer | Quelle |
| --- | --- | --- |
| Konto und alles daran Hängende | bis zur Löschung in der App; dann sofort | `backend/internal/store` `DeleteUser`, D39 |
| Sign-in-with-Apple-Token (verschlüsselt) | nach Löschung bis Apple den Widerruf bestätigt (Wiederholung alle 15 Min.) | `store.go`, API.md |
| Zufällige Konto-ID | dauerhaft in Löschvermerk, Admin-Protokoll (`admin_audit`, nur anhängend, mit den Details der Admin-Handlung) und Freischaltlisten von Testfunktionen | D88, `0007`, `0014`, `0022`; `store.go` `DeleteUser` |
| Supabase-Anmeldesitzungen und -ereignisse (IP, User-Agent; im Auth-Schema) | **offen**: Dauer, und ob die Kontolöschung sie entfernt | Supabase-Standard, im Repo nicht sichtbar; **prüfen** |
| Cloud-Trace-Spuren (URL, Zeiten; Stichprobe) | **offen**: ob gespeichert, Dauer (Google-Standard angeblich 30 Tage) | `infra/gcp/project.tf:13` (API an); **prüfen** |
| Backups (Supabase Pro) | 7 Tage | OPERATIONS → Backups; **PITR aus: prüfen** |
| Logs (eigene und Google, Bucket `ryft-logs`) | 14 Tage, Belgien | `infra/gcp/logging.tf` |
| Supabase-eigene Logs (mit IP) | laut Plan, Pro 7 Tage *(Suche)* | **prüfen** |
| Ereignisse Pub/Sub / Fehlgeschlagene | 7 Tage / 14 Tage | `pubsub.auto.tfvars.json` |
| Outbox (gesendet) | 7 Tage | `outbox.go` |
| App-Attest-Schlüssel / Challenges | bis Löschung, Ersatz oder 90 Tage ohne Nutzung / 5 Minuten gültig, gelöscht beim nächsten 15-Minuten-Takt (nur Hash) | `0037`, `appattest.go`, `scheduler.tf`, D247 |
| An Einladung gebundene E-Mail und Notiz | 30 Tage nach Ende der Einlösbarkeit (Code höchstens 90 Tage) | D159, D161 |
| Notiz an einem nicht gebundenen Code (Freitext des Betreibers) | keine Frist; wird nie gelöscht | `backend/internal/store/beta.go:324-332`, D161 |
| Anmelde-Mail-Kopien (Gesendete Elemente) | 7 Tage, danach 14 Tage wiederherstellbar | D239, OPERATIONS 14a.4; **Einstellung prüfen** |
| Support-Mails | **offen** (Marker) | – |
| PostHog-Ereignisse | **offen** (Marker) | PostHog-Projekteinstellung |
| Ausstehender Import auf dem iPhone | bis 30 Tage | wie bisher |
| Feedback | bis zur Kontolöschung (keine eigene Frist) | `store.go`, API.md |

## 5. Fragen an den Anwalt

1. **Verantwortlicher ohne Privatangaben (Abschnitt 0 der Checkliste, U1):** Wie lassen sich Art. 13 Abs. 1
   lit. a DSGVO (und § 5 DDG im Impressum) erfüllen, ohne Wohnanschrift, private E-Mail und Telefon zu
   veröffentlichen? Reicht eine ladungsfähige Service-Anschrift, oder braucht es eine Gesellschaft (z. B. UG)?
   *Hinweis:* Die geltende Fassung 2026-10-26 enthält Name und Anschrift; der neue Text nicht mehr.
2. **Englisch allein (D230):** Genügt eine nur englische Datenschutzerklärung (und ein nur englischer
   Einwilligungstext in der App) für Nutzer in Deutschland, insbesondere für eine ausdrückliche Einwilligung
   nach Art. 9 Abs. 2 lit. a in Verbindung mit Art. 12 Abs. 1 DSGVO?
3. **Datenschutzbeauftragter (U2):** Ist bei einer Privatperson mit wenigen Testern und Gesundheitsdaten
   (Art. 37 Abs. 1 lit. c DSGVO, § 38 BDSG) einer zu benennen?
4. **Aufsichtsbehörde (U4):** Welche ist zuständig? Die geltende Fassung nennt das BayLDA; der neue Text hat
   einen Marker, weil die Behörde vom Sitz des Verantwortlichen abhängt (Frage 1).
5. **Mindestalter 16** (App und Server erzwingen 16 beim Geburtsjahr, das aber optional ist): ausreichend,
   oder braucht es eine echte Altersabfrage?
6. **Datenschutz-Folgenabschätzung (Art. 35):** Bei Gesundheitsdaten in kleinem Umfang erforderlich?
   Gibt es ein Verzeichnis der Verarbeitungstätigkeiten (Art. 30), das wir vorbereiten sollen?
7. **App Attest:** Trägt das berechtigte Interesse (Art. 6 Abs. 1 lit. f) die Geräteprüfung? Ist das Erzeugen
   und Nutzen eines Schlüssels auf dem iPhone nach § 25 Abs. 2 Nr. 2 TDDDG „unbedingt erforderlich“, oder
   braucht es eine Einwilligung?
8. **Werte in Push-Mitteilungen** (z. B. „Recovery 82 %“ laufen über Apples Push-Dienst): von der
   Art.-9-Einwilligung gedeckt, oder gesondert zu erwähnen bzw. abschaltbar zu machen?
9. **Apples Rolle** (Sign in with Apple, Push, App Attest, TestFlight): eigener Verantwortlicher,
   Auftragsverarbeiter oder nur Hinweis? Ist der Text dazu ausreichend?
10. **Admin-Zugriff:** Der Betreiber sieht E-Mail-Adressen, Geräte, Einwilligungen, Feedback und hat Zugriff auf
    Datenbank und Cloud-Projekt. Ist der Satz in §8 ausreichend? Der Einwilligungsbildschirm sagt: „Only you,
    and the operator of RYFT when something needs fixing.“
11. **Drittlandübermittlungen:** Google (DPF + SCC), Supabase (SCC, Singapur), PostHog (US-Mutter, DPF nicht
    gefunden), Microsoft (über GoDaddy): Sind die Formulierungen und Grundlagen so tragfähig? Marker in §5.
12. **Microsoft über GoDaddy:** Gilt Microsofts DPA, wenn das Abo über GoDaddy läuft? Ist GoDaddy als
    Wiederverkäufer mit möglichem Admin-Zugang zum Mandanten zu nennen?
13. **AVV-Abschlüsse:** Für Google Cloud (CDPA), Firebase, PostHog und Microsoft ist kein Abschluss
    dokumentiert. Reicht die Annahme in der Konsole, und was muss vor Veröffentlichung vorliegen?
14. **Neue Fassung, neue Einwilligung:** Der Wechsel 2026-10-26 → 2026-11-20 fordert jeden Nutzer zur erneuten
    Einwilligung auf. Ist das richtig so, oder reicht für Teile (Infrastrukturwechsel AWS → Google) eine
    Information?
15. **Datenübertragbarkeit (Art. 20):** Der Export in der App enthält Konto, Tage, Workouts und
    Trainingseinheiten, aber nicht Journal, Pläne, Einwilligungen, Feedback, Push-Tokens und App-Attest-Schlüssel
    (D245; „anything else on request“ im Text). Genügt das?
16. **Gesundheitsdaten nur auf Einwilligung, Konto auf Vertrag:** Ist die Trennung in §4 so tragfähig,
    insbesondere für Journal-Einträge (Alkohol, Cannabis, Krankheit)?
17. **Einladungsanfragen per Mail** (#129): Die Website hat einen Mail-Link „Ask for an invite“; die Adresse
    kann an einen Code gebunden und 30 Tage nach dessen Ende gelöscht werden. Muss die anfragende Person
    beim Erheben (Art. 13) gesondert informiert werden, z. B. mit einem Link in der Antwortmail?

**Offene `TODO(user)` im Text (19):**
1. Verantwortlicher (§1, U1)
2. DSB erforderlich? (§1, U2)
3. Ereignisliste und Kennung der Produktanalyse (§3, aus dem Analyse-Plan der App)
4. Menüpfad zum Abschalten der Analyse (§3, sobald die App ihn hat)
5. Grundlage für den Schlüssel auf dem iPhone, § 25 TDDDG (§4, Frage 7)
6. Cloud Trace: gespeichert, wie lange (§5)
7. Drittland-Grundlage Google Cloud (§5, Frage 11)
8. Bedingungen und Grundlage Firebase Hosting (§5)
9. Log-Aufbewahrung bei Supabase (§5)
10. Drittland-Grundlage Supabase (§5, Frage 11)
11. PostHog: GeoIP und Sitzungsaufzeichnung (§5)
12. PostHog: Aufbewahrung (§5)
13. PostHog: Drittland-Grundlage (§5, Frage 11)
14. Microsoft: Datenstandort, Bedingungen, Grundlage (§5, Frage 12)
15. GoDaddy: Zugriff auf die Postfächer (§5, Frage 12)
16. Supabase-Anmeldesitzungen und -ereignisse: Dauer, Löschung (§6)
17. Aufbewahrung der Support-Mails (§6)
18. PostHog: Aufbewahrung und Löschung mit dem Konto (§6)
19. Aufsichtsbehörde (§7, U4)

Dazu kommt der Marker im `draftNote` (die Antworten dieses Pakets): 20 `TODO(user)` in 14 Zeilen der Datei.

## 6. Vom Betreiber in der Konsole zu bestätigen (keine Rechtsfragen)

- Google Cloud: der Bucket `_Default` ist abgeschaltet (`infra/gcp/logging.tf`); Firebase Hosting:
  Cloud-Logging-Integration aus; ob Cloud Trace Anfragespuren speichert (die API ist an, `project.tf`), sonst
  abschalten oder nennen; der CDPA ist angenommen.
- Supabase: Plan Pro, PITR aus, Log-Aufbewahrung; was `auth.sessions` und `auth.audit_log_entries` (IP,
  User-Agent, E-Mail) halten, wie lange, und ob sie nach einer Kontolöschung leer sind (SQL-Editor);
  kein Restore-Projekt mit Echtdaten übrig (D239, Drill);
  die alte Irland-Instanz gelöscht (Checkliste S1); welcher SMTP-Weg live ist (Microsoft oder Ausweichanbieter,
  OPERATIONS 14a).
- Microsoft 365: Datenstandort des Mandanten; Aufbewahrung 7 Tage auf „Gesendete Elemente“ von `no-reply@`
  (14a.4 Schritt 5); `RetainDeletedItemsFor` 14 Tage.
- PostHog: „Discard client IP data“ an; GeoIP; Sitzungsaufzeichnung aus; Aufbewahrung; Personenprofile; DPA.
- Monitoring: die E-Mail-Benachrichtigung geht an eine Adresse des Betreibers, nicht an Dritte.

## 7. Technische Befunde (keine Rechtsfragen, vor Veröffentlichung zu beheben oder zu nennen)

- `feature_flags.allow_user_ids` behält die Konto-ID nach der Löschung (`DeleteUser` entfernt sie nicht). Der
  Text nennt das jetzt in §6; besser wäre, die Löschung zu erweitern (Backend-Ticket) und den Satz zu streichen.
- Die Notiz an einem nicht gebundenen Einladungscode wird nie gelöscht (`beta.go:324-332`); sie ist Freitext
  des Betreibers und kann eine Person nennen. Der Text spricht nur von gebundenen Codes.
- Die Ratenbegrenzung räumt nicht genutzte Einträge nur alle 1024 Anfragen auf (`ratelimit.go`); bei wenig
  Verkehr bleibt ein Hash bis zum Ende der Serverinstanz im Speicher. Der Text sagt das so.
- Der Löschtest (`delete_test.go`, `owned()`) zählt Einwilligungen, Journal, Pläne und `apple_tokens` nicht mit;
  `DeleteUser` löscht sie laut Code, der Test belegt es nur nicht.
- Die App-Kopie `ryft-app-main` (Stand 2026-11-17) verlinkt noch `ryftapp.de`, die Arbeitskopie `ryftlab.de`
  (siehe `app-texts.md`).
- Die App (Arbeitskopie) hat manuelle Aktivitäten mit optionaler GPS-Route und Notizen (`PUT /v1/activities`)
  und manuellen Schlaf (`PUT /v1/sleep/manual`); die Plattform hat diese Endpunkte noch nicht. Sobald er kommt, braucht es eine neue Fassung.
