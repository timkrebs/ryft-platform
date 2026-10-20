package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// appleRelayDomain is where Sign in with Apple's "Hide My Email" addresses live.
const appleRelayDomain = "privaterelay.appleid.com"

// maskEmail keeps the first letter of the name and of the domain and the
// top-level domain: tim@gmail.com → t***@g***.com.
func maskEmail(e string) string {
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" || domain == "" {
		return "***"
	}
	tld := ""
	if i := strings.LastIndex(domain, "."); i > 0 {
		domain, tld = domain[:i], domain[i:]
	}
	first := func(s string) string { r, _ := utf8.DecodeRuneInString(s); return string(r) }
	return first(local) + "***@" + first(domain) + "***" + tld
}

func isRelay(e string) bool { return strings.HasSuffix(strings.ToLower(e), "@"+appleRelayDomain) }

// shown is an email as the admin view shows it: masked unless emails was asked for.
func shown(e string, emails bool) *string {
	if e == "" {
		return nil
	}
	if !emails {
		e = maskEmail(e)
	}
	return &e
}

// userDetail is GET /v1/admin/users/{id}[?emails=1]: the account with its
// sign-in identities (from Supabase), devices, data and possible duplicates.
func (a admin) userDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := a.resolve(w, r, "id")
	if !ok {
		return
	}
	emails := r.URL.Query().Get("emails") == "1"
	auditTarget(r, id)
	auditDetail(r, "emails", emails)
	d, err := a.h.st.AdminUserDetail(r.Context(), id)
	if err != nil {
		metrics.AdminRequests.WithLabelValues("error").Inc()
		httpx.Fail(w, r, err)
		return
	}
	acct := &d.Account
	if acct.Email != nil {
		acct.PrivateRelay = isRelay(*acct.Email)
		acct.Email = shown(*acct.Email, emails)
	}
	a.addIdentities(r.Context(), acct, emails)
	metrics.AdminRequests.WithLabelValues("ok").Inc()
	a.h.log.InfoContext(r.Context(), "admin user read", "target_user", acct.ShortID, "emails", emails,
		"duplicates", len(d.Duplicates), "admin_key", a.keyID)
	httpx.WriteJSON(w, 200, d)
}

// addIdentities asks Supabase how the account signs in; without the admin key
// (apikey mode, or SUPABASE_SERVICE_ROLE_KEY unset) the view says so.
func (a admin) addIdentities(ctx context.Context, acct *model.AdminAccount, emails bool) {
	if a.h.accounts == nil || a.h.accounts.admin == nil {
		acct.IdentitiesNote = "not available: no Supabase admin key on the api"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := a.h.accounts.admin.GetUser(ctx, acct.ID)
	switch {
	case errors.Is(err, auth.ErrNoAuthUser):
		acct.Identities, acct.IdentitiesNote = []model.AdminIdentity{}, "no Supabase auth user with this id"
		return
	case err != nil:
		a.h.log.WarnContext(ctx, "supabase admin: get user", "err", err)
		acct.IdentitiesNote = "Supabase did not answer: " + err.Error()
		return
	}
	acct.LastSignInAt = info.LastSignInAt
	acct.Identities = []model.AdminIdentity{}
	for _, i := range info.Identities {
		acct.Identities = append(acct.Identities, model.AdminIdentity{Provider: i.Provider, Email: shown(i.Email, emails),
			PrivateRelay: isRelay(i.Email), LastSignInAt: i.LastSignInAt})
	}
}
