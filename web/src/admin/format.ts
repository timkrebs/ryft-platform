// Small display helpers.

const pad = (n: number) => String(n).padStart(2, '0');

/** "2026-11-05 18:02" in the operator's time zone; "—" for null; the input itself when it is no date. */
export function fmtTime(iso: string | null | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** An Apple "Hide My Email" address. */
export function isRelay(email: string | null | undefined): boolean {
  return !!email && /@privaterelay\.appleid\.com$/i.test(email);
}

/** RFC 3339 (no fractions) for `days` from `from`. */
export function inDays(days: number, from: number): string {
  return new Date(from + days * 86_400_000).toISOString().replace(/\.\d{3}Z$/, 'Z');
}

export function daysAgo(days: number, from: number): string {
  return inDays(-days, from);
}

/** Case-insensitive "contains" over several fields. */
export function matches(query: string, ...fields: (string | null | undefined)[]): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return fields.some((f) => !!f && f.toLowerCase().includes(q));
}
