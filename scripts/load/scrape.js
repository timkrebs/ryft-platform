// k6 script for scripts/load.sh: fetch Prometheus text from the services' (unpublished) metrics ports over the
// compose network and write it to /out/<name>.prom. The fetch is in handleSummary: it runs in its own runtime,
// so nothing the default function stored is visible there (and it may use k6/http).
// SCRAPE="ingest=http://ingest:9090/metrics,scoring=http://scoring:9090/metrics"
import http from 'k6/http';

export const options = { vus: 1, iterations: 1 };
const targets = (__ENV.SCRAPE || '').split(',').filter(Boolean).map((p) => [p.slice(0, p.indexOf('=')), p.slice(p.indexOf('=') + 1)]);

export default function () {}

export function handleSummary() {
  const out = {};
  for (const [name, url] of targets) {
    const r = http.get(url, { tags: { name: 'scrape' } });
    out['/out/' + name + '.prom'] = r.status === 200 ? r.body : '';
  }
  return out;
}
