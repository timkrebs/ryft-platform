# Was sich gegenüber der geltenden Fassung 2026-10-26 ändert

In einfachen Worten, Abschnitt für Abschnitt. Die geltende Fassung ist
`web/content/legal/en/privacy/2026-10-26.mdx` (in Kraft seit 2026-11-01); der Entwurf ist
`privacy-2026-11-20.mdx` in diesem Ordner. Quellen je Punkt in `facts.md`.

## Was wegfällt, weil es nicht mehr existiert

- **Amazon Web Services** (Server, Logs, DNS, Load Balancer in Frankfurt): abgeschaltet und gelöscht (D246).
  Ersetzt durch Google Cloud in Belgien.
- **Grafana Labs** (Grafana Cloud, London): seit D236 nicht mehr genutzt.
- **Google Workspace** für support@: Die Mail läuft seit D214 über Microsoft 365.
- „credentials are kept in a secrets vault“ (Vault): ersetzt durch Googles Secret Manager.
- „the scoring service also logs the daily scores“: Die Bewertung protokolliert keine Werte mehr
  (`docs/OPERATIONS.md` → Data protection).
- Ereignisse „for notifications also your morning scores“: Ereignisse tragen keine Werte mehr
  (`backend/internal/model/events.go`).
- Das **Zugriffsprotokoll der Website** und die **Hell/Dunkel-Wahl im Browser** (samt § 25 TDDDG dafür): Die
  Website hat kein eigenes Protokoll mehr und speichert nichts im Browser (web D19, D20).
- „to limit requests per address“ für die Website: Firebase Hosting hat keine solche Begrenzung durch RYFT.

## Was sich ändert

- **§1:** Name und Anschrift des Verantwortlichen sind durch einen Marker ersetzt (Checkliste Abschnitt 0).
  Der Datenschutzbeauftragte bleibt „keiner“, mit einer Frage.
- **§2:** Das Schreiben in Apple Health enthält keine Energie (die App schreibt nur Art, Beginn und Ende);
  „closed beta“ wird zu „free beta … invited testers only“.
- **§3, Konto:** Anmeldung auch per E-Mail und Passwort bei Supabase genannt; das Apple-Token als
  „verschlüsselt gespeichert“.
- **§3, Gesundheitsdaten:** neu die Kennung, die Apple Health jedem Eintrag gibt (gegen Doppelzählung).
- **§3, Berechnetes:** neu „welche von zwei Aufzeichnungen desselben Workouts zählt“.
- **§3, Geräte:** neu die Zusammenfassung der letzten Synchronisation, wann eine Mitteilung zuletzt ankam und
  welche Mitteilungen gesendet wurden.
- **§3, Feedback:** die Diagnose wird vollständig aufgezählt (einschließlich Sprache und Region und der letzten
  20 Anfragen), mit dem Hinweis, dass sie abschaltbar ist und keine Gesundheitswerte enthält.
- **§3, Protokolle:** Pfade können ein Datum oder eine Kennung enthalten; Googles Protokolle speichern die
  IP-Adresse, URL und User-Agent, aber nur bei blockierten oder markierten Anfragen und bei Serverfehlern
  (D242); die IP-Ratenbegrenzung hält nur einen Hash im Speicher (D240).
- **§3, Website:** Firebase Hosting (Google) statt des eigenen Servers; kein Protokoll, nichts im Browser.
- **§4:** neue Grundlagen für App Attest (berechtigtes Interesse, mit Frage zu § 25 TDDDG), Produktanalyse
  (Einwilligung), Einladungen und Anmelde-Mails (Vertrag); Website nur noch lit. f.
- **§5:** neu Google Cloud (Belgien, mit Cloud Trace als Marker), Firebase Hosting, PostHog, Microsoft, GoDaddy;
  Supabases eigene Logs und Anmeldeaufzeichnungen mit IP; die Drittland-Grundlage für Supabase ist jetzt ein Marker
  statt einer Feststellung; bei Apple neu App Attest und TestFlight-Feedback; Push-Texte können auch
  Trainingsergebnisse enthalten.
- **§6:** neue Fristen: Logs 14 Tage in Belgien (statt „14 days“ ohne Ort), interne Ereignisse 7 bzw. 14 Tage,
  App-Attest-Schlüssel und Challenges, Einladungsadressen, Kopien der Anmelde-Mails, Support-Mails und PostHog
  (Marker). Was nach der Löschung bleibt, ist genauer: das Apple-Token bis zur Bestätigung, die Konto-Kennung
  auch im Admin-Protokoll.
- **§7:** Die Aufsichtsbehörde (bisher BayLDA) ist ein Marker; der Export nennt seinen Umfang.
- **§8:** Secret Manager statt Vault, Google statt AWS; neu die Firewall, die Geräteprüfung und was der
  Betreiber in den Admin-Werkzeugen sieht.
- **Werbung/Analyse:** „no advertising or analytics SDKs“ wird zu „no advertising SDKs; die einzige Analyse ist
  die Produktanalyse mit Einwilligung“.

## Was neu hinzukommt

- **App Attest** (§3, §4, §5, §6): Geräteprüfung mit Apple; was gespeichert wird, wie lange.
- **Produktanalyse mit PostHog** (§3, §4, §5, §6): nur mit Einwilligung, pseudonym, ohne Gesundheitsdaten, ohne
  gespeicherte IP, in Frankfurt.
- **E-Mails** (§3, §5, §6): Einladungsanfragen, an Codes gebundene Adressen (#129), Anmelde-Mails über Microsoft
  mit Kopie in „Gesendete Elemente“.

## Was gleich bleibt

§9 (Alter 16), §10 (keine automatisierten Entscheidungen, kein Medizinprodukt), §11 (neue Fassung, neue
Einwilligung), die Liste der Gesundheitsdatenarten, der Import, die Einwilligungsnachweise, Supabase in Frankfurt
mit Backups von 7 Tagen und die Rechte in §7.
