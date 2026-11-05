// The two origins the admin page talks to, from config/admin.json. scripts/csp.mjs reads
// the same file for the page's connect-src, so the code and the policy cannot drift. All
// three values are public by design (the project ref is in every token's `iss` and in the
// app binary; the publishable key is meant for clients).
import raw from '../../config/admin.json';

export interface AdminConfig {
  apiUrl: string;
  supabaseUrl: string;
  supabaseKey: string;
}

export const config: AdminConfig = raw;
