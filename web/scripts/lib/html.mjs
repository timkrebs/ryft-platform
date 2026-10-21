// HTML helpers for the postbuild and the CSP script. Next's export is regular,
// machine-written HTML, so tag-level regular expressions are enough here.
import { createHash } from 'node:crypto';

const SCRIPT = /<script\b([^>]*)>([\s\S]*?)<\/script>/gi;

/** Removes every script without `data-ryft` and every script preload. */
export function stripRuntime(html) {
  let removed = 0;
  let out = html.replace(SCRIPT, (tag, attrs) => {
    if (/\bdata-ryft\b/.test(attrs)) return tag;
    removed++;
    return '';
  });
  out = out.replace(/<link\b[^>]*\brel="(?:modulepreload|preload)"[^>]*\bas="script"[^>]*\/?>/gi, () => {
    removed++;
    return '';
  });
  out = out.replace(/<link\b[^>]*\bas="script"[^>]*\brel="preload"[^>]*\/?>/gi, () => {
    removed++;
    return '';
  });
  return { html: out, removed };
}

/** The inline scripts of a page: `{attrs, body}` for every <script> without `src`. */
export function inlineScripts(html) {
  return [...html.matchAll(SCRIPT)]
    .filter(([, attrs]) => !/\bsrc\s*=/.test(attrs))
    .map(([, attrs, body]) => ({ attrs, body }));
}

/** The CSP source expression for an inline script body: 'sha256-…'. */
export function cspHash(body) {
  return `'sha256-${createHash('sha256').update(body, 'utf8').digest('base64')}'`;
}
