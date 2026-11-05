// The api at api.ryftapp.de/v1/admin/*, called with the operator's access token.
//   401  → refresh the session once and send again; a second 401 ends the session.
//   403 mfa_required → ApiError with that code (the caller shows the TOTP challenge).
//   no answer (CORS missing, an edge 429, a 502) → ApiError.network.
import { AuthError } from './gotrue';
import type { Sessions } from './sessions';

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    /** The `code` of the error body, e.g. mfa_required. */
    readonly code: string | null,
    readonly requestId: string | null,
    /** Retry-After in seconds, when sent. */
    readonly retryAfter: number | null,
    /** The browser saw no answer at all. */
    readonly network = false,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

/** The session is gone (no token, or the refresh token was refused). Sign in again. */
export class SessionEnded extends Error {
  constructor() {
    super('session ended');
    this.name = 'SessionEnded';
  }
}

export interface ApiDeps {
  baseUrl: string;
  sessions: Sessions;
  fetch?: typeof fetch;
  /** Called after every successful answer. */
  onOk?: () => void;
}

export type AdminApi = ReturnType<typeof createApi>;

export function createApi(deps: ApiDeps) {
  const doFetch: typeof fetch = deps.fetch ?? ((input, init) => globalThis.fetch(input, init));

  async function send(method: string, path: string, token: string, body: unknown): Promise<Response> {
    const headers: Record<string, string> = { Authorization: `Bearer ${token}`, Accept: 'application/json' };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    try {
      return await doFetch(`${deps.baseUrl}${path}`, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        credentials: 'omit',
        cache: 'no-store',
        referrerPolicy: 'no-referrer',
      });
    } catch {
      throw new ApiError(
        'No answer from the api (network or edge limit). Try again.',
        0,
        null,
        null,
        null,
        true,
      );
    }
  }

  async function fresh() {
    try {
      return await deps.sessions.fresh();
    } catch (e) {
      if (e instanceof AuthError) throw new ApiError(e.message, 0, null, null, null, true);
      throw e;
    }
  }

  async function request<T = undefined>(method: string, path: string, body?: unknown): Promise<T> {
    let s = await fresh();
    if (!s) throw new SessionEnded();
    let res = await send(method, path, s.access_token, body);
    if (res.status === 401) {
      try {
        s = await deps.sessions.refresh();
      } catch (e) {
        if (e instanceof AuthError) throw new ApiError(e.message, 0, null, null, null, true);
        throw e;
      }
      if (!s) throw new SessionEnded();
      res = await send(method, path, s.access_token, body);
      if (res.status === 401) {
        deps.sessions.clear();
        throw new SessionEnded();
      }
    }
    if (!res.ok) throw await toError(res);
    deps.onOk?.();
    if (res.status === 204) return undefined as T;
    try {
      return (await res.json()) as T;
    } catch {
      throw new ApiError(
        'The api sent an answer this page does not understand.',
        res.status,
        null,
        requestId(res, null),
        null,
      );
    }
  }

  return {
    get: <T>(path: string) => request<T>('GET', path),
    post: <T = undefined>(path: string, body?: unknown) => request<T>('POST', path, body),
    del: (path: string) => request('DELETE', path),
  };
}

function requestId(res: Response, body: Record<string, unknown> | null): string | null {
  const fromBody = body && typeof body.request_id === 'string' ? body.request_id : null;
  return fromBody ?? res.headers.get('X-Request-ID');
}

async function toError(res: Response): Promise<ApiError> {
  let body: Record<string, unknown> | null = null;
  try {
    const parsed: unknown = await res.json();
    if (typeof parsed === 'object' && parsed !== null) body = parsed as Record<string, unknown>;
  } catch {
    body = null;
  }
  const retry = Number(res.headers.get('Retry-After'));
  return new ApiError(
    typeof body?.error === 'string' && body.error ? body.error : `The api answered ${res.status}.`,
    res.status,
    typeof body?.code === 'string' ? body.code : null,
    requestId(res, body),
    Number.isFinite(retry) && retry > 0 ? retry : null,
  );
}

/** A message for the person at the form (a missing field, a malformed code); shown as it is. */
export class FormError extends Error {}

/** One line for the screen: the message, the status, the request id, the wait. All of it shown as text. */
export function describeError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.network) return 'No answer from api.ryftapp.de (network or edge limit). Try again.';
    const parts = [e.message];
    if (e.status === 429 && e.retryAfter) parts.push(`Try again in ${e.retryAfter} s.`);
    if (e.status === 503 && e.retryAfter) parts.push(`Try again in ${e.retryAfter} s.`);
    parts.push(`(${e.status}${e.requestId ? `, request id ${e.requestId}` : ''})`);
    return parts.join(' ');
  }
  if (e instanceof AuthError || e instanceof FormError) return e.message;
  return 'Something went wrong. Try again.';
}
