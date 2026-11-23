package connect

import (
	"net/url"
	"strings"
	"testing"
)

// RFC 7636, appendix B.
func TestChallengeRFC7636Vector(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := Challenge(verifier); got != want {
		t.Fatalf("Challenge = %q, want %q", got, want)
	}
}

func TestSecretsHaveTheirForm(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s, tk, v := NewState(), NewTicket(), NewVerifier()
		if !ValidState(s) || len(s) != 64 {
			t.Fatalf("state %q", s)
		}
		if !ValidTicket(tk) || len(tk) != 32 {
			t.Fatalf("ticket %q", tk)
		}
		if len(v) != 43 || strings.ContainsAny(v, "+/=") {
			t.Fatalf("verifier %q is no 43-character base64url", v)
		}
		if seen[s] || seen[tk] || seen[v] {
			t.Fatal("a secret repeated")
		}
		seen[s], seen[tk], seen[v] = true, true, true
	}
}

func TestValidStateAndTicketRefuseOtherForms(t *testing.T) {
	for _, s := range []string{"", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("g", 64), "--" + strings.Repeat("a", 62)} {
		if ValidState(s) {
			t.Errorf("ValidState(%q)", s)
		}
	}
	for _, s := range []string{"", strings.Repeat("F", 32), strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("x", 32)} {
		if ValidTicket(s) {
			t.Errorf("ValidTicket(%q)", s)
		}
	}
}

func TestHashIsSHA256OfTheText(t *testing.T) {
	// SHA-256("abc"), FIPS 180-4.
	h := Hash("abc")
	if got := hexOf(h[:]); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("Hash(abc) = %s", got)
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

// P2-10: the method is S256, set by the helper every adapter uses, and CheckPKCE refuses anything else.
func TestPKCEMethodIsPinnedToS256(t *testing.T) {
	if ChallengeMethod != "S256" {
		t.Fatalf("ChallengeMethod = %q", ChallengeMethod)
	}
	ch := Challenge(NewVerifier())
	q := url.Values{"client_id": {"x"}}
	SetPKCE(q, ch)
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != ch {
		t.Fatalf("SetPKCE: %v", q)
	}
	// A caller that set "plain" first is overwritten, not added to.
	q = url.Values{"code_challenge_method": {"plain"}}
	SetPKCE(q, ch)
	if got := q["code_challenge_method"]; len(got) != 1 || got[0] != "S256" {
		t.Fatalf("method after SetPKCE: %v", got)
	}
	good := "https://provider.example/authorize?" + q.Encode()
	if err := CheckPKCE(good, ch); err != nil {
		t.Fatalf("CheckPKCE(good): %v", err)
	}
	for name, raw := range map[string]string{
		"plain":           "https://p.example/a?code_challenge=" + ch + "&code_challenge_method=plain",
		"no method":       "https://p.example/a?code_challenge=" + ch,
		"no challenge":    "https://p.example/a?code_challenge_method=S256",
		"other challenge": "https://p.example/a?code_challenge=zzz&code_challenge_method=S256",
		"method twice":    "https://p.example/a?code_challenge=" + ch + "&code_challenge_method=S256&code_challenge_method=plain",
	} {
		if err := CheckPKCE(raw, ch); err == nil {
			t.Errorf("%s: CheckPKCE accepted %s", name, raw)
		}
	}
	if CheckPKCE(good, "") == nil {
		t.Error("an empty challenge passed")
	}
}
