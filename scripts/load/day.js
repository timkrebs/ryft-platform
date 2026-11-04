// k6 script for scripts/load.sh. N synthetic users, each: consent, ONE sync of a full day of Apple-Watch-like
// records (the shape of scripts/synth.py), then READS times GET /v1/days over 30 days with a think time.
// Everything is a function of (user index, ANCHOR): the same bytes every run, no wall clock inside the test.
// The users are "<API_KEY>:<uuid>" bearers (internal/auth/apikey.go), so one compose stack holds N accounts.
// Env (set by load.sh): INGEST_URL API_URL API_KEY ANCHOR (YYYY-MM-DD, the day synced) RUN (user set) USERS READS
// RAMP_MS THINK_MS MAX_ERROR_RATE [P95_SYNC_MS P95_DAYS_MS], and OUT (default /out/summary.json).
import http from 'k6/http';
import { sleep } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';

const env = (k, d) => (__ENV[k] !== undefined && __ENV[k] !== '' ? __ENV[k] : d);
const USERS = parseInt(env('USERS', '50'), 10);
const READS = parseInt(env('READS', '20'), 10);
const RAMP_MS = parseInt(env('RAMP_MS', '100'), 10);
const THINK_MS = parseInt(env('THINK_MS', '250'), 10);
const RUN = parseInt(env('RUN', '0'), 10);
const ANCHOR = env('ANCHOR', '');
const INGEST_URL = env('INGEST_URL', 'http://ingest:8080');
const API_URL = env('API_URL', 'http://api:8080');
const API_KEY = env('API_KEY', 'dev-api-key-change-me');
const MAX_ERR = parseFloat(env('MAX_ERROR_RATE', '0.01'));
const OUT = env('OUT', '/out/summary.json');

const syncMs = new Trend('ryft_load_sync_ms', true);
const daysMs = new Trend('ryft_load_days_ms', true);
const syncFail = new Rate('ryft_load_sync_fail');
const daysFail = new Rate('ryft_load_days_fail');
const consentFail = new Rate('ryft_load_consent_fail');
const limited = new Counter('ryft_load_http_429');
const recordsSent = new Counter('ryft_load_records_sent');

// The error-rate thresholds make k6 exit 99. The optional latency ones are the hook for the nightly job.
const thresholds = {
  ryft_load_sync_fail: ['rate<=' + MAX_ERR],
  ryft_load_days_fail: ['rate<=' + MAX_ERR],
  ryft_load_consent_fail: ['rate<=' + MAX_ERR],
};
if (env('P95_SYNC_MS', '')) thresholds.ryft_load_sync_ms = ['p(95)<' + env('P95_SYNC_MS', '')];
if (env('P95_DAYS_MS', '')) thresholds.ryft_load_days_ms = ['p(95)<' + env('P95_DAYS_MS', '')];

export const options = {
  scenarios: {
    users: { executor: 'per-vu-iterations', vus: USERS, iterations: 1, maxDuration: '10m' },
  },
  thresholds,
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
};

// ---- deterministic data -------------------------------------------------------------------------------
function mulberry32(a) { // small seeded PRNG; the generator below never calls Math.random
  return function () {
    a |= 0; a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
function gauss(r, mu, sd) { // Box-Muller
  const u = 1 - r(), v = r();
  return mu + sd * Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v);
}
const hex12 = (n) => ('000000000000' + n.toString(16)).slice(-12);
// uuid-shaped, unique per (run, user index): a run gets fresh accounts, so every measured run inserts rather than de-duplicates.
function userId(run, i) { return '10ad0000-0000-4000-8000-' + hex12(run * 100000 + i + 1); }
const deviceId = (i) => 'de1c0000-0000-4000-8000-' + hex12(i + 1);
const MIN = 60 * 1000;
const iso = (ms) => new Date(ms).toISOString().replace('.000Z', 'Z');

// One complete UTC day D: the night ending the morning of D (bed ~23:00 on D-1), overnight HRV/respiration/SpO2,
// resting HR, heart rate every minute from waking to midnight, steps and active energy in 10-minute buckets
// until 22:00 (plus the phone's 30-minute step buckets, so source de-duplication has work), a run at 17:30.
// A full day, not "until now": the record count does not depend on the hour the test starts.
function dayPayload(i, anchor) {
  const r = mulberry32(1000 + i);
  const act = mulberry32(7000 + i);
  const day0 = Date.parse(anchor + 'T00:00:00Z');
  const samples = [], sleep = [], workouts = [];
  const add = (type, s, e, v, unit, source) =>
    samples.push({ type, start: iso(s), end: iso(e), value: Math.round(v * 100) / 100, unit, source: source || 'synth' });
  const bed = day0 - 60 * MIN - Math.floor(r() * 41) * MIN;
  const wake = day0 + 6 * 60 * MIN + (30 + Math.floor(r() * 46)) * MIN;
  sleep.push({ start: iso(bed), end: iso(wake), stage: 'inbed', source: 'synth' });
  const weights = [50, 20, 25, 5], stages = ['core', 'deep', 'rem', 'awake'];
  for (let t = bed + 12 * MIN; t < wake;) {
    let x = r() * 100, k = 0;
    while (k < 3 && x >= weights[k]) { x -= weights[k]; k++; }
    const end = Math.min(t + (10 + Math.floor(r() * 51)) * MIN, wake);
    sleep.push({ start: iso(t), end: iso(end), stage: stages[k], source: 'synth' });
    t = end;
  }
  add('hrv_sdnn', wake - 120 * MIN, wake - 120 * MIN, Math.max(20, gauss(r, 52, 6)), 'ms');
  add('respiratory_rate', wake - 180 * MIN, wake - 180 * MIN, gauss(r, 14.5, 0.6), 'brpm');
  add('spo2', wake - 180 * MIN, wake - 180 * MIN, gauss(r, 96.5, 0.8), '%');
  add('resting_hr', day0 + 480 * MIN, day0 + 480 * MIN, gauss(r, 50, 2), 'bpm');
  const ws = day0 + (17 * 60 + 30) * MIN, we = ws + [40, 55, 70][Math.floor(r() * 3)] * MIN;
  workouts.push({ id: 'a0c00000-0000-4000-8000-' + hex12(i + 1), activity: 'running', start: iso(ws), end: iso(we), source: 'synth' });
  for (let t = wake; t < day0 + 1440 * MIN; t += MIN) {
    add('heart_rate', t, t, Math.max(40, t >= ws && t <= we ? gauss(r, 152, 8) : gauss(r, 68, 6)), 'bpm');
  }
  for (let t = wake; t < day0 + 22 * 60 * MIN; t += 10 * MIN) {
    const moving = t >= ws && t <= we;
    add('steps', t, t + 10 * MIN, moving ? 1200 + Math.floor(act() * 501) : Math.floor(act() * 181), 'count');
    add('active_energy', t, t + 10 * MIN, moving ? 90 + act() * 40 : 2 + act() * 7, 'kcal');
    if ((t - wake) % (30 * MIN) === 0) add('steps', t, t + 30 * MIN, Math.floor(act() * 501), 'count', 'synth-phone');
  }
  return { device_id: deviceId(i), device_name: 'synth', samples, sleep, workouts };
}

const authHeaders = (i) => ({ Authorization: 'Bearer ' + API_KEY + ':' + userId(RUN, i), 'Content-Type': 'application/json' });
const addDays = (day, n) => new Date(Date.parse(day + 'T00:00:00Z') + n * 86400000).toISOString().slice(0, 10);

export function setup() {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(ANCHOR)) throw new Error('ANCHOR (YYYY-MM-DD) is required');
  const res = http.get(API_URL + '/v1/config', { headers: authHeaders(0), tags: { name: 'setup_config' } });
  if (res.status !== 200) throw new Error('GET /v1/config: ' + res.status);
  return { policy: res.json('privacy_policy_version') };
}

export default function (data) {
  const i = __VU - 1;
  const h = authHeaders(i);
  // Consent first (POST /v1/sync needs it); not one of the measured routes.
  const c = http.post(API_URL + '/v1/me/consent', JSON.stringify({ policy_version: data.policy, app_version: 'load' }),
    { headers: h, tags: { name: 'consent' } });
  consentFail.add(c.status >= 300);
  sleep((i * RAMP_MS) / 1000); // a deterministic stagger instead of 50 simultaneous uploads

  const body = dayPayload(i, ANCHOR);
  recordsSent.add(body.samples.length + body.sleep.length + body.workouts.length);
  const s = http.post(INGEST_URL + '/v1/sync', JSON.stringify(body), { headers: h, tags: { name: 'sync' } });
  syncMs.add(s.timings.duration);
  syncFail.add(s.status !== 200);
  if (s.status === 429) limited.add(1);

  const q = API_URL + '/v1/days?from=' + addDays(ANCHOR, -29) + '&to=' + ANCHOR;
  for (let n = 0; n < READS; n++) {
    sleep(THINK_MS / 1000); // below the per-user limit of 10 req/s (internal/ratelimit): measures the service, not the limiter
    const d = http.get(q, { headers: h, tags: { name: 'days' } });
    daysMs.add(d.timings.duration);
    daysFail.add(d.status !== 200);
    if (d.status === 429) limited.add(1);
  }
}

export function handleSummary(data) {
  const v = (name) => (data.metrics[name] ? data.metrics[name].values : {});
  const route = (trend, fail) => ({
    count: (v(fail).passes || 0) + (v(fail).fails || 0), // a Rate counts every request
    p50_ms: v(trend).med, p95_ms: v(trend)['p(95)'], p99_ms: v(trend)['p(99)'], max_ms: v(trend).max, avg_ms: v(trend).avg,
    error_rate: v(fail).rate,
  });
  const failed = Object.keys(data.metrics).some((k) => {
    const t = data.metrics[k].thresholds;
    return t && Object.keys(t).some((x) => t[x].ok === false);
  });
  const out = {
    schema: 1, anchor: ANCHOR, run: RUN, users: USERS, reads_per_user: READS, think_ms: THINK_MS, ramp_ms: RAMP_MS,
    duration_s: data.state.testRunDurationMs / 1000,
    records_per_sync: USERS ? (v('ryft_load_records_sent').count || 0) / USERS : 0,
    http_429: v('ryft_load_http_429').count || 0,
    consent_error_rate: v('ryft_load_consent_fail').rate,
    routes: {
      'POST /v1/sync': route('ryft_load_sync_ms', 'ryft_load_sync_fail'),
      'GET /v1/days': route('ryft_load_days_ms', 'ryft_load_days_fail'),
    },
    thresholds_ok: !failed,
  };
  return { [OUT]: JSON.stringify(out, null, 2) };
}
