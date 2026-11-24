package connect

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
)

// The OAuth flow's secrets: the state that binds a callback to the account that started it, the PKCE verifier
// (RFC 7636, method S256) and the confirm ticket. The state and the ticket are stored as their SHA-256 only; the
// verifier is sealed by the api.

// StateBytes and TicketBytes are the random bytes of a state and a ticket; both are shown as hex.
const (
	StateBytes  = 32
	TicketBytes = 16
)

var (
	stateRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ticketRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("connect: no randomness: " + err.Error()) // crypto/rand does not fail on a working system
	}
	return b
}

// NewState is a state: 32 random bytes as 64 lower-case hex characters. Hex, so that Cloud Armor's sqli rule
// (libinjection's "--", D247) cannot match our half of the callback's query.
func NewState() string { return hex.EncodeToString(randomBytes(StateBytes)) }

// ValidState says whether s has the form of a state.
func ValidState(s string) bool { return stateRe.MatchString(s) }

// NewTicket is a confirm ticket: 16 random bytes as 32 lower-case hex characters.
func NewTicket() string { return hex.EncodeToString(randomBytes(TicketBytes)) }

// ValidTicket says whether s has the form of a ticket.
func ValidTicket(s string) bool { return ticketRe.MatchString(s) }

// Hash is the SHA-256 a state or a ticket is stored as.
func Hash(s string) [32]byte { return sha256.Sum256([]byte(s)) }

// NewVerifier is a PKCE code verifier: 32 random bytes as 43 base64url characters (RFC 7636 section 4.1).
func NewVerifier() string { return base64.RawURLEncoding.EncodeToString(randomBytes(32)) }

// Challenge is the S256 code challenge of a verifier: BASE64URL(SHA256(verifier)) without padding.
func Challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ChallengeMethod is the one PKCE method RYFT uses (RFC 7636): S256. "plain" would put the verifier itself on the
// authorize URL, which is in the browser's history and the provider's logs.
const (
	ChallengeMethod = "S256"
	ParamChallenge  = "code_challenge"
	ParamMethod     = "code_challenge_method"
)

// SetPKCE adds the PKCE parameters to an authorize URL's query: code_challenge and code_challenge_method=S256.
// Every adapter's AuthorizeURL builds its query with it, so no adapter can send another method (P2-10); the phase-2
// adapter inherits the rule.
func SetPKCE(q url.Values, codeChallenge string) {
	q.Set(ParamChallenge, codeChallenge)
	q.Set(ParamMethod, ChallengeMethod)
}

// CheckPKCE is the conformance check for an adapter's tests: raw carries exactly the given code challenge and the
// method S256, once each.
func CheckPKCE(raw, codeChallenge string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	q := u.Query()
	if got := q[ParamMethod]; len(got) != 1 || got[0] != ChallengeMethod {
		return errors.New("connect: the authorize URL does not carry code_challenge_method=S256")
	}
	if got := q[ParamChallenge]; len(got) != 1 || got[0] != codeChallenge || codeChallenge == "" {
		return errors.New("connect: the authorize URL does not carry the code challenge")
	}
	return nil
}
