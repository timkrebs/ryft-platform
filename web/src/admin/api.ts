// The api at api.ryftlab.de/v1/admin/*, called with the operator's access token.
//   401  → refresh the session once and send again; a second 401 ends the session (and revokes
//          its refresh token, best effort).
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
  /**
   * Called with the access token when a second 401 ends the session, so its refresh token is
   * revoked too (GoTrue's logout, scope local). Best effort: a failure is ignored.
   */
  revoke?: (accessToken: string) => Promise<void>;
}

const NO_ANSWER = 'No answer from api.ryftlab.de (network or edge limit). Try again.';

/** The sign-in service did not decide about the refresh token: the session is kept, try again. */
function authUndecided(e: AuthError): ApiError {
  const message =
    e.status === 0
      ? e.message
      : e.status === 429
        ? 'The sign-in service asks to wait before renewing the session. You are still signed in; try again in a moment.'
        : `The sign-in service could not renew the session (${e.status}). You are still signed in; try again.`;
  return new ApiError(message, 0, null, null, null, true);
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
      throw new ApiError(NO_ANSWER, 0, null, null, null, true);
    }
  }

  async function fresh() {
    try {
      return await deps.sessions.fresh();
    } catch (e) {
      if (e instanceof AuthError) throw authUndecided(e);
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
        if (e instanceof AuthError) throw authUndecided(e);
        throw e;
      }
      if (!s) throw new SessionEnded();
      res = await send(method, path, s.access_token, body);
      if (res.status === 401) {
        deps.sessions.clear();
        // Best effort and not awaited: the tab forgets the session either way.
        const token = s.access_token;
        void Promise.resolve()
          .then(() => deps.revoke?.(token))
          .catch(() => undefined);
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

/** The api's messages are lower-case phrases ("too many requests"); a hint after one needs a full stop. */
function sentence(text: string): string {
  return /[.!?]$/.test(text) ? text : `${text}.`;
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
    if (e.network) return e.message;
    // A 429 always says when to try again: Retry-After, or 1 s when the answer had none.
    const wait = e.status === 429 ? (e.retryAfter ?? 1) : e.status === 503 ? e.retryAfter : null;
    const parts = [wait ? sentence(e.message) : e.message];
    if (wait) parts.push(`Try again in ${wait} s.`);
    parts.push(`(${e.status}${e.requestId ? `, request id ${e.requestId}` : ''})`);
    return parts.join(' ');
  }
  if (e instanceof AuthError || e instanceof FormError) return e.message;
  return 'Something went wrong. Try again.';
}
