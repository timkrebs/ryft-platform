// The shapes the page reads, from docs/API.md → Admin routes. Only the fields the page
// renders are declared; for a token the server already leaves the rest out.

export interface Session {
  access_token: string;
  refresh_token: string;
  /** Unix seconds. */
  expires_at: number;
}

export interface Actor {
  kind: 'key' | 'user';
  id: string | null;
}

export interface InviteUse {
  user_id: string;
  short_id: string;
  email: string | null;
  private_relay: boolean;
  at: string;
}

export type InviteStatus = 'open' | 'used' | 'expired' | 'revoked';

export interface BetaInvite {
  code: string;
  note: string;
  max_uses: number;
  uses: number;
  expires_at: string | null;
  created_at: string;
  email: string | null;
  created_by: Actor;
  revoked_at: string | null;
  status: InviteStatus;
  link: string;
  uses_by: InviteUse[];
}

export interface InviteInput {
  note?: string;
  email?: string;
  max_uses: number;
  expires_at?: string;
}

/** What an operator's token gets of the users list. */
export interface AdminUserBrief {
  id: string;
  short_id: string;
  created_at: string;
  provider: string;
  email?: string | null;
  beta_access: boolean;
  last_sync_at: string | null;
}

/** What an operator's token gets of the feedback list. */
export interface FeedbackBrief {
  id: number;
  user_short_id: string;
  category: string;
  message: string;
  created_at: string;
  resolved_at: string | null;
}

export interface FeatureFlag {
  key: string;
  enabled: boolean;
  rollout_pct: number;
  allow_user_ids: string[] | null;
  updated_at: string;
}

export interface AuditRecord {
  id: number;
  at: string;
  actor: Actor;
  action: string;
  target_user: string | null;
  status: number;
  request_id: string;
  details: Record<string, unknown> | null;
}
