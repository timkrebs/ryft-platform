// The brand matcher behind scripts/check-brand.mjs and scripts/brand-report.mjs.
// Pure: text in, findings out. The denylist lives in config/brand-denylist.json.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const DENYLIST = fileURLToPath(new URL('../../config/brand-denylist.json', import.meta.url));

// Characters that render as nothing but split a word for a naive search.
const INVISIBLE = /[­͏؜ᅟᅠ឴឵᠋-᠏​-‏‪-‮⁠-⁯ㅤ︀-️﻿ﾠ]/g;

const NAMED_ENTITIES = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ', shy: '' };

/** Text as a reader sees it: entities and escapes decoded, accents and invisible characters gone. */
export function normalise(text) {
  return text
    .replace(/&#x([0-9a-f]{1,6});?/gi, (_, h) => safeChar(parseInt(h, 16)))
    .replace(/&#(\d{1,7});?/g, (_, d) => safeChar(parseInt(d, 10)))
    .replace(/&([a-z]+);/gi, (m, n) => NAMED_ENTITIES[n.toLowerCase()] ?? m)
    .replace(/\\u\{([0-9a-f]{1,6})\}|\\u([0-9a-f]{4})|\\x([0-9a-f]{2})/gi, (_, a, b, c) =>
      safeChar(parseInt(a ?? b ?? c, 16)),
    )
    .normalize('NFKD')
    .replace(/\p{M}/gu, '')
    .replace(INVISIBLE, '');
}

function safeChar(code) {
  return code > 0 && code <= 0x10ffff ? String.fromCodePoint(code) : '';
}

/** Compiles the denylist: `{hard: Rule[], warn: Rule[], allow: Allow[]}`. */
export function loadDenylist(path = DENYLIST) {
  const raw = JSON.parse(readFileSync(path, 'utf8'));
  const compile = (level) => (r) => ({ ...r, level, regex: new RegExp(r.pattern, `g${r.flags ?? 'iu'}`) });
  return {
    hard: raw.hard.map(compile('hard')),
    warn: raw.warn.map(compile('warn')),
    allow: raw.allow ?? [],
    warnIgnore: raw.warnIgnore ?? [],
  };
}

/**
 * Every match in `text`, line by line. For HTML-like files the tag-stripped line is
 * checked too, so markup between letters doesn't hide a match.
 * Returns `{level, id, note, line, column, match, context}`.
 */
export function scanText(text, denylist, { file = '', markup = false } = {}) {
  const findings = [];
  const lines = text.split(/\r?\n/);
  const rules = [...denylist.hard, ...denylist.warn];
  lines.forEach((raw, i) => {
    const variants = [normalise(raw)];
    if (markup) variants.push(normalise(raw.replace(/<[^>]*>/g, '')));
    const seen = new Set();
    for (const rule of rules) {
      for (const line of variants) {
        rule.regex.lastIndex = 0;
        for (const m of line.matchAll(rule.regex)) {
          const key = `${rule.id}:${m.index}`;
          if (seen.has(key)) continue;
          seen.add(key);
          if (isAllowed(denylist, rule, file, raw)) continue;
          findings.push({
            level: rule.level,
            id: rule.id,
            note: rule.note,
            line: i + 1,
            column: (m.index ?? 0) + 1,
            match: m[0],
            context: line.trim().slice(0, 240),
          });
        }
      }
    }
  });
  return findings;
}

// An allow entry silences one warning in one file (`file` is a substring of the path,
// `contains` a substring of the line); `warnIgnore` lists files whose warnings are
// fixtures. Hard rules can't be allowed.
function isAllowed({ allow = [], warnIgnore = [] }, rule, file, line) {
  if (rule.level === 'hard') return false;
  if (warnIgnore.includes(file)) return true;
  return allow.some(
    (a) => a.id === rule.id && file.includes(a.file) && (!a.contains || line.includes(a.contains)),
  );
}

/** A finding's text with every hard match masked, for reports that must not repeat it. */
export function redact(text, denylist) {
  let out = normalise(text);
  for (const rule of denylist.hard) {
    rule.regex.lastIndex = 0;
    out = out.replace(rule.regex, (m) => '▇'.repeat(Math.min(m.length, 5)));
  }
  return out;
}

const BINARY =
  /\.(png|jpe?g|gif|webp|avif|ico|icns|woff2?|ttf|otf|eot|pdf|zip|gz|br|mp4|mov|car|a|o|dylib|xcuserstate|DS_Store)$/i;

/** Whether a path is worth reading as text. */
export function isTextPath(path) {
  return !BINARY.test(path);
}

/** Whether to also scan tag-stripped lines. */
export function isMarkupPath(path) {
  return /\.(html?|xml|svg|mdx?|xcstrings|plist)$/i.test(path);
}
