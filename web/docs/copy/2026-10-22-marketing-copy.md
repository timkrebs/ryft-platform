# ryftapp.de — copy revision 2026-10-22 (EN + DE)

Source: `web/messages/en.json` and `de.json` as deployed on 2026-10-22. Only the keys below change; every other key
stays as it is. Facts, numbers, formulas, weights, ranges and app paths are **unchanged** — this is a voice
revision, not a content change. Legal pages (`content/legal/`), the science page body (`content/science/`) and
the licences page are out of scope.

**Voice for the new copy:** benefit first, then the fact that backs it. Short sentences, active verbs, "you/du".
Confident, never hype: no "revolutionary", "unlock", "game-changer", no superlatives we can't prove, no medical
promises. German is written in German, not translated: du-form, natural word order, typographic dashes and quotes
(„…“), non-breaking space before % and units.

---

## Meta, navigation, footer, 404

| Key | EN | DE |
| --- | --- | --- |
| `site.tagline` | Your whole health. *(unchanged)* | Deine Gesundheit. Ganz klar. |
| `site.description` | RYFT turns the data your watch or ring already collects into four clear daily scores: Readiness, Recovery, Strain and Sleep. Open formulas, hosted in the EU. | RYFT macht aus den Daten deiner Uhr oder deines Rings vier klare Werte für jeden Tag: Bereitschaft, Erholung, Belastung und Schlaf. Offene Formeln, gehostet in der EU. |
| `footer.noTracking` | No cookies. No analytics. No third-party scripts. | Keine Cookies. Keine Analyse. Keine Skripte von Dritten. |
| `notFound.body` | This page doesn't exist – or not anymore. Check the link, or start again from the home page. | Diese Seite gibt es nicht – oder nicht mehr. Prüf den Link oder starte neu auf der Startseite. |
| `home.title` | RYFT – Your whole health, in four numbers | RYFT – Deine Gesundheit in vier Zahlen |

## Hero

| Key | EN | DE |
| --- | --- | --- |
| `home.hero.eyebrow` | For iPhone · Now in beta | Für iPhone · Jetzt in der Beta |
| `home.hero.title` | Your whole health. *(unchanged)* | Deine Gesundheit. Ganz klar. |
| `home.hero.lead` | Your watch already measures your heart rate, HRV and sleep. RYFT turns it into four clear numbers every morning – and shows you exactly how each one is calculated. | Deine Uhr misst Herzfrequenz, HRV und Schlaf längst. RYFT macht daraus jeden Morgen vier klare Zahlen – und zeigt dir genau, wie jede davon entsteht. |
| `home.hero.primary` | Join the beta *(unchanged)* | Bei der Beta mitmachen |
| `home.hero.secondary` | See how it works | So funktioniert's *(unchanged)* |
| `home.hero.facts[0]` | Hosted in the EU *(unchanged)* | Gehostet in der EU *(unchanged)* |
| `home.hero.facts[1]` | No new device | Kein neues Gerät |
| `home.hero.facts[2]` | Free in the beta | Kostenlos in der Beta |
| `home.today.sample` | Sample data, shown the way RYFT's Today screen displays it. | Beispieldaten, dargestellt wie auf dem Heute-Bildschirm der App. |

## How it works

| Key | EN | DE |
| --- | --- | --- |
| `home.how.title` | From the watch you wear to one clear number | Von deiner Uhr zu einer klaren Zahl |
| `home.how.intro` | No new device to buy or charge. RYFT works with the watch or ring you already own. | Kein neues Gerät, das du kaufen oder laden musst. RYFT arbeitet mit der Uhr oder dem Ring, die du schon hast. |
| `home.how.steps[0].title` | Wear what you already have | Trag, was du schon hast |
| `home.how.steps[0].body` | Apple Watch, Garmin, Oura – any watch or ring that writes heart rate and sleep to Apple Health. (An iPhone on its own counts steps, but can't measure heart rate.) | Apple Watch, Garmin, Oura – jede Uhr und jeder Ring, die Herzfrequenz und Schlaf in Apple Health schreiben. (Ein iPhone allein zählt Schritte, misst aber keine Herzfrequenz.) |
| `home.how.steps[1].title` | You decide what RYFT reads | Du entscheidest, was RYFT liest |
| `home.how.steps[1].body` | Pick the data types, give your consent, done. RYFT syncs in the background – encrypted, to servers in Frankfurt. | Datenarten wählen, einwilligen, fertig. RYFT synchronisiert im Hintergrund – verschlüsselt, auf Server in Frankfurt. |
| `home.how.steps[2].title` | Wake up to your scores | Morgens deine Werte |
| `home.how.steps[2].body` | RYFT compares last night with your personal baseline and turns it into Readiness: one number that tells you how much to take on today. | RYFT vergleicht die letzte Nacht mit deinem persönlichen Ausgangswert und macht daraus deine Bereitschaft: eine Zahl, die dir sagt, wie viel du dir heute vornehmen kannst. |

## The scores

| Key | EN | DE |
| --- | --- | --- |
| `home.scores.title` | Four numbers. No black box. | Vier Zahlen. Keine Blackbox. |
| `home.scores.intro` | Every score is built from published inputs, weights and ranges – and every result names the model version that calculated it. | Jeder Wert beruht auf veröffentlichten Eingaben, Gewichten und Bereichen – und jedes Ergebnis nennt die Modellversion, die es berechnet hat. |
| `home.scores.range` | Scale | Skala |
| `home.scores.bands` | What it means | Einordnung |
| `home.scores.more` | See the full formulas | Alle Formeln ansehen |
| `home.scores.items.readiness.measures` | How much you can take on today – the first number you see in the morning. | Wie viel du dir heute zumuten kannst – die erste Zahl, die du morgens siehst. |
| `home.scores.items.readiness.note` | Up to 10 points off after a hard day on low recovery. | Bis zu 10 Punkte Abzug nach einem harten Tag bei niedriger Erholung. |
| `home.scores.items.recovery.measures` | How well your body recovered overnight – measured against your own normal, not an average. | Wie gut sich dein Körper über Nacht erholt hat – gemessen an deinem eigenen Normalwert, nicht an einem Durchschnitt. |
| `home.scores.items.recovery.note` | Each signal is compared with your last 30 days. | Jedes Signal im Vergleich zu deinen letzten 30 Tagen. |
| `home.scores.items.strain.measures` | How hard your heart worked today – from an easy walk to a race. | Wie hart dein Herz heute gearbeitet hat – vom Spaziergang bis zum Wettkampf. |
| `home.scores.items.sleep.measures` | How much of the sleep you needed you actually got. | Wie viel deines Schlafbedarfs du tatsächlich geschlafen hast. |
| `home.scores.items.sleep.inputs[1][0]` | Your need: learned baseline + strain + sleep debt − naps | Dein Bedarf: gelernter Grundbedarf + Belastung + Schlafschuld − Nickerchen |
| `home.scores.items.sleep.note` | Sleep quality also weighs efficiency and restorative sleep: 60 % performance, 20 % efficiency, 20 % deep and REM. | Die Schlafqualität berücksichtigt zusätzlich Effizienz und erholsamen Schlaf: 60 % Leistung, 20 % Effizienz, 20 % Tief- und REM-Schlaf. |
| `home.scores.items.sleep.bands[2][1]` | Poor *(unchanged)* | Schwach |

The readiness, recovery and strain **band labels** stay as they are: they must match the app's own wording
(`../app/Ryft/Features/Today/ReadinessCopy.swift`, `Localizable.xcstrings`). If they differ from the app today,
align the site to the app, not the other way round.

## Beyond the scores

| Key | EN | DE |
| --- | --- | --- |
| `home.features.label` | More than scores | Mehr als Werte *(unchanged)* |
| `home.features.title` | Train. Journal. Insights. | Training. Journal. Insights. |
| `home.features.train.body` | Plans, sessions and sets with a rest timer, last time's numbers and your personal records. Every logged session counts toward your day's strain. | Pläne, Einheiten und Sätze – mit Pausentimer, deinen Werten vom letzten Mal und deinen Bestleistungen. Jede erfasste Einheit zählt in deine Tagesbelastung. |
| `home.features.journal.body` | Answer a few quick questions each morning – alcohol, caffeine, stress, feeling sick. After five days with and five without, RYFT shows how each habit changes your next-morning recovery, with a 90 % interval instead of a guess. | Beantworte morgens ein paar kurze Fragen – Alkohol, Koffein, Stress, Krankheit. Nach fünf Tagen mit und fünf ohne zeigt RYFT, wie jede Gewohnheit deine Erholung am nächsten Morgen verändert – mit einem 90-%-Intervall statt Bauchgefühl. |
| `home.features.journal.card` | Morning after *(unchanged)* | Am Morgen danach |
| `home.features.insights.body` | Clear, plain-language cards when something changes – your HRV climbing above baseline, your bedtime drifting later. Trends over 7, 30 and 90 days, always measured against you. | Klare Karten in einfacher Sprache, sobald sich etwas tut – deine HRV steigt über den Ausgangswert, deine Schlafenszeit rutscht nach hinten. Trends über 7, 30 und 90 Tage, immer gemessen an dir selbst. |

## Principles

| Key | EN | DE |
| --- | --- | --- |
| `home.principles.title` | What RYFT stands for | Wofür RYFT steht |
| `home.principles.items[0].body` | Your scores come from formulas you can read. If AI-written text ever arrives, it will be opt-in – and always below the numbers. | Deine Werte entstehen aus Formeln, die du nachlesen kannst. Kommt je KI-Text dazu, dann nur auf Wunsch – und immer unter den Zahlen. |
| `home.principles.items[1].title` | Every score explained | Jeder Wert erklärt |
| `home.principles.items[1].body` | Inputs, weights, baselines and ranges for every score – all on the science page. | Eingaben, Gewichte, Ausgangswerte und Bereiche für jeden Wert – alles auf der Wissenschaftsseite. |
| `home.principles.items[2].body` | Every score is stamped with the model version that calculated it. When the model changes, a changelog shows what moved. | Jeder Wert trägt die Modellversion, die ihn berechnet hat. Ändert sich das Modell, zeigt ein Änderungsprotokoll, was sich verschoben hat. |
| `home.principles.items[3].body` | Export your account, scores and workouts as JSON, or delete your account right in the app – anytime, instantly. | Exportiere Konto, Werte und Trainings als JSON oder lösche dein Konto direkt in der App – jederzeit, sofort. |
| `home.principles.items[4].title` | Honest about uncertainty | Ehrlich bei Unsicherheit |
| `home.principles.items[4].body` | Your first 7 nights are marked as calibrating. Habit effects come with an interval and stay locked until there's enough data. | Deine ersten 7 Nächte sind als Kalibrierung markiert. Effekte von Gewohnheiten kommen mit Intervall und bleiben gesperrt, bis genug Daten da sind. |

## Privacy

| Key | EN | DE |
| --- | --- | --- |
| `home.privacy.title` | Private by default | Privat von Anfang an |
| `home.privacy.items[0].title` | Hosted in Frankfurt | Gehostet in Frankfurt |
| `home.privacy.items[0].body` | RYFT's servers and database run in Frankfurt, Germany. The privacy policy names every service provider. | Server und Datenbank von RYFT laufen in Frankfurt am Main. Die Datenschutzerklärung nennt jeden Dienstleister. |
| `home.privacy.items[1].title` | Health data only with your consent | Gesundheitsdaten nur mit deiner Einwilligung |
| `home.privacy.items[1].body` | RYFT asks before the first upload – and you can withdraw your consent in the app at any time. | RYFT fragt vor der ersten Übertragung – und du kannst deine Einwilligung jederzeit in der App widerrufen. |
| `home.privacy.items[2].title` | No ads. No tracking. | Keine Werbung. Kein Tracking. |
| `home.privacy.items[3].title` | Export or delete in seconds | In Sekunden exportieren oder löschen |

## FAQ

| Key | EN | DE |
| --- | --- | --- |
| `home.faq.title` | Frequently asked questions | Häufige Fragen |
| `home.faq.items[1].a` | No. RYFT is free during the beta – no subscription, no in-app purchases. | Nein. RYFT ist in der Beta kostenlos – kein Abo, keine In-App-Käufe. |
| `home.faq.items[3].q` | How soon are the scores accurate? | Ab wann stimmen die Werte? |
| `home.faq.items[3].a` | Strain works from day one – even better if you add your birth year or a measured maximum heart rate in the app. Recovery and Readiness learn your personal baseline: until you have 7 nights, RYFT uses population values and marks the day as calibrating. | Die Belastung funktioniert ab dem ersten Tag – noch genauer, wenn du in der App dein Geburtsjahr oder deine gemessene maximale Herzfrequenz einträgst. Erholung und Bereitschaft lernen deinen persönlichen Ausgangswert: Bis 7 Nächte da sind, nutzt RYFT Bevölkerungswerte und markiert den Tag als Kalibrierung. |
| `home.faq.items[6].a` | That works. When two sources record the same hour, RYFT takes the one with more readings instead of adding them up. For a night, it takes the source that measured the most sleep. | Das geht. Zeichnen zwei Quellen dieselbe Stunde auf, nimmt RYFT die mit mehr Messungen, statt sie zu addieren. Für eine Nacht nimmt es die Quelle, die am meisten Schlaf gemessen hat. |
| `home.faq.items[9].q` | How do I join? *(unchanged)* | Wie kann ich mitmachen? |

## Closing call to action

| Key | EN | DE |
| --- | --- | --- |
| `home.cta.title` | See your health in four numbers | Deine Gesundheit in vier Zahlen |
| `home.cta.body` | Free on TestFlight. All you need is an invite code. | Kostenlos über TestFlight. Du brauchst nur einen Einladungscode. |
| `home.cta.button` | Join the beta *(unchanged)* | Bei der Beta mitmachen |

## Beta page

| Key | EN | DE |
| --- | --- | --- |
| `beta.description` | Join the RYFT beta on TestFlight: who it's for, what you need, and how to get started. | Mach bei der RYFT-Beta auf TestFlight mit: für wen sie ist, was du brauchst und wie du startest. |
| `beta.heading` | Join the RYFT beta | Mach bei der RYFT-Beta mit |
| `beta.lead` | RYFT is in a closed beta on TestFlight, Apple's app for testing new apps. It's free, it gets new versions regularly – and your feedback shapes what comes next. Some parts may still be rough around the edges. | RYFT läuft als geschlossene Beta über TestFlight, Apples App zum Testen neuer Apps. Sie ist kostenlos, bekommt regelmäßig neue Versionen – und dein Feedback bestimmt mit, was als Nächstes kommt. Manches ist noch nicht ganz rund. |
| `beta.who.items[2]` | You're curious about your numbers – and happy to tell us when one looks off. | Du bist neugierig auf deine Zahlen – und sagst uns Bescheid, wenn eine nicht stimmt. |
| `beta.steps.items[0].body` | The beta is invite-only. Ask the friend who told you about RYFT, or email us. | Die Beta ist nur auf Einladung. Frag die Person, die dir von RYFT erzählt hat, oder schreib uns. |
| `beta.testers.title` | What we ask of testers | Was wir uns von dir wünschen |
| `beta.testers.items[1]` | Compare the scores with how you feel – and tell us when they don't match. | Vergleich die Werte mit deinem Gefühl – und sag uns, wenn sie nicht zusammenpassen. |
| `beta.noForm` | No forms here: the button opens your mail app, and we only use your address to reply. | Hier gibt es kein Formular: Der Button öffnet dein Mailprogramm, und wir nutzen deine Adresse nur für die Antwort. |

## App-link fallback

| Key | EN | DE |
| --- | --- | --- |
| `open.body` | This link opens a screen in the RYFT app. You're seeing this page because RYFT isn't installed here – or you opened the link on another device. | Dieser Link öffnet einen Bildschirm in der RYFT-App. Du siehst diese Seite, weil RYFT hier nicht installiert ist – oder du den Link auf einem anderen Gerät geöffnet hast. |
| `open.beta` | RYFT is an iPhone app, currently in beta on TestFlight. | RYFT ist eine iPhone-App, aktuell in der Beta über TestFlight. |

## Science page chrome (labels only)

| Key | EN | DE |
| --- | --- | --- |
| `science.model` | Scoring model {model} · checked {date} *(unchanged)* | Berechnungsmodell {model} · geprüft am {date} |
