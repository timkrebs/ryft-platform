// A hint for the page only: the payload of the access token, read without any key. The
// server verifies the token; this decides what to show and never grants anything.

export interface TokenClaims {
  aal: 'aal1' | 'aal2' | null;
  /** app_metadata.role when it is a string: the only place the admin role is read from. */
  appRole: string | null;
  exp: number | null;
}

export function decodeClaims(token: string): TokenClaims | null {
  const parts = token.split('.');
  if (parts.length !== 3) return null;
  try {
    const b64 = parts[1]!.replace(/-/g, '+').replace(/_/g, '/');
    const bytes = Uint8Array.from(atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4)), (c) => c.charCodeAt(0));
    const p: unknown = JSON.parse(new TextDecoder().decode(bytes));
    if (typeof p !== 'object' || p === null) return null;
    const o = p as Record<string, unknown>;
    const meta =
      typeof o.app_metadata === 'object' && o.app_metadata !== null
        ? (o.app_metadata as Record<string, unknown>)
        : {};
    return {
      aal: o.aal === 'aal1' || o.aal === 'aal2' ? o.aal : null,
      appRole: typeof meta.role === 'string' ? meta.role : null,
      exp: typeof o.exp === 'number' ? o.exp : null,
    };
  } catch {
    return null;
  }
}
