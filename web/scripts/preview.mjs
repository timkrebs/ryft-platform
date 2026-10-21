#!/usr/bin/env node
// A quick local preview of out/ without nginx: the same lookup as nginx/default.conf's
// try_files ($uri, $uri.html, $uri/index.html, then 404.html). For the real thing, with
// headers, CSP and the app-link routes, use `npm run serve` (the nginx image).
//
//   node scripts/preview.mjs [port]
import { createReadStream, existsSync, statSync } from 'node:fs';
import { createServer } from 'node:http';
import { extname, join, normalize, resolve } from 'node:path';

const OUT = resolve(import.meta.dirname, '../out');
const port = Number(process.argv[2] ?? 4321);
const types = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css',
  '.js': 'text/javascript',
  '.json': 'application/json',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.webp': 'image/webp',
  '.avif': 'image/avif',
  '.woff2': 'font/woff2',
  '.xml': 'application/xml',
  '.txt': 'text/plain; charset=utf-8',
  '.webmanifest': 'application/manifest+json',
};

createServer((req, res) => {
  const path = normalize(decodeURIComponent(new URL(req.url ?? '/', 'http://x').pathname));
  const appLink = /^\/(day|sleep|insights|train|auth|invite)(\/|$)/.test(path);
  const candidates = appLink
    ? ['/open.html']
    : [path, `${path}.html`, join(path, 'index.html'), path === '/' ? '/index.html' : ''];
  for (const c of candidates.filter(Boolean)) {
    const file = join(OUT, c);
    if (file.startsWith(OUT) && existsSync(file) && statSync(file).isFile()) {
      const type = c.includes('.well-known/apple-app-site-association')
        ? 'application/json'
        : types[extname(file)];
      res.writeHead(200, { 'content-type': type ?? 'application/octet-stream' });
      createReadStream(file).pipe(res);
      return;
    }
  }
  res.writeHead(404, { 'content-type': 'text/html; charset=utf-8' });
  createReadStream(join(OUT, '404.html')).pipe(res);
}).listen(port, () => console.log(`preview: http://localhost:${port}`));
