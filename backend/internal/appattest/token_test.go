package appattest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	testUser  = uuid.MustParse("6f1c2b3a-4d5e-4f60-8a7b-9c0d1e2f3a4b")
	otherUser = uuid.MustParse("0b9a8c7d-6e5f-4a3b-9c2d-1e0f9a8b7c6d")
)

// testRingLine is one ring line with kid n (as 8 hex digits) and the 32 bytes
// first, first+1, ...: built at run time, never a literal (gitleaks).
func testRingLine(first byte, n uint32) string {
	k := make([]byte, ringKeyLen)
	for i := range k {
		k[i] = first + byte(i)
	}
	return fmt.Sprintf("%08x:%s", n, base64.StdEncoding.EncodeToString(k))
}

func mustRing(t *testing.T, lines ...string) *Ring {
	t.Helper()
	r, err := ParseRing(strings.Join(lines, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func testKeyID() [32]byte {
	var id [32]byte
	for i := range id {
		id[i] = byte(0xff - i)
	}
	return id
}

func TestTokenTTL(t *testing.T) {
	// AA3: the app renews with 300 s left; a token must outlive that by far.
	if TokenTTL < MinTokenTTL {
		t.Fatalf("TokenTTL %v below MinTokenTTL %v", TokenTTL, MinTokenTTL)
	}
	if TokenTTL != 3600*time.Second || MinTokenTTL != 600*time.Second || ChallengeTTL != 5*time.Minute {
		t.Fatalf("TTLs moved: token %v, min %v, challenge %v (API.md: ttl_sec 3600, challenge 5 min)", TokenTTL, MinTokenTTL, ChallengeTTL)
	}
}

// The format is pinned (D245) for the ring bytes 0..31 under kid 0a1b2c3d, the
// fixed user, the fixed id of testKeyID and the time testNow.
const (
	goldenBody = "bxwrOk1eT2CKe5wNHi86SxhlwAgx5z9-4j_BPLLQ9YgAAAAAawAaIAAAAABrACgw"
	goldenMAC  = "Tz3d6Y3K7PbMUjdFiHG25hjCftQs3Jg6TnMZSnl6DSY"
)

func TestTokenGolden(t *testing.T) {
	r := mustRing(t, testRingLine(0, 0x0a1b2c3d))
	got, exp := r.Mint(testUser, testKeyID(), testNow)
	want := "v1.0a1b2c3d." + goldenBody + "." + goldenMAC
	if got != want {
		t.Errorf("token\n got %s\nwant %s", got, want)
	}
	if !exp.Equal(testNow.Add(time.Hour)) || exp.Location() != time.UTC {
		t.Errorf("expires %v", exp)
	}
	if len(got) != 120 || len(got) > maxTokenLen {
		t.Errorf("length %d, want 120", len(got))
	}

	// The body's layout, field by field, and the MAC's input.
	parts := strings.Split(got, ".")
	body, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(body) != 48 {
		t.Fatalf("body %v %d", err, len(body))
	}
	kid := testKeyID()
	kh := sha256.Sum256(kid[:])
	if string(body[:16]) != string(testUser[:]) || string(body[16:32]) != string(kh[:16]) ||
		int64(binary.BigEndian.Uint64(body[32:40])) != testNow.Unix() ||
		int64(binary.BigEndian.Uint64(body[40:48])) != testNow.Unix()+3600 {
		t.Errorf("body layout: %x", body)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte("ryft-app-token/v1/0a1b2c3d." + parts[2]))
	if parts[3] != base64.RawURLEncoding.EncodeToString(h.Sum(nil)) {
		t.Error("the MAC is not HMAC-SHA256 of ryft-app-token/v1/<kid>.<body>")
	}
}

func TestCheckReasons(t *testing.T) {
	r := mustRing(t, testRingLine(0x40, 1))
	tok, _ := r.Mint(testUser, testKeyID(), testNow)
	parts := strings.Split(tok, ".")
	join := func(p ...string) string { return strings.Join(p, ".") }
	flip := func(s string) string { // change the last character within the alphabet
		last := s[len(s)-1]
		repl := byte('A')
		if last == 'A' {
			repl = 'B'
		}
		return s[:len(s)-1] + string(repl)
	}
	otherKid := mustRing(t, testRingLine(0x40, 2))
	tokOtherKid, _ := otherKid.Mint(testUser, testKeyID(), testNow)
	sameKidOtherKey := mustRing(t, testRingLine(0x60, 1))
	tokForged, _ := sameKidOtherKey.Mint(testUser, testKeyID(), testNow)
	future, _ := r.Mint(testUser, testKeyID(), testNow.Add(61*time.Second))
	edge, _ := r.Mint(testUser, testKeyID(), testNow.Add(60*time.Second))

	cases := []struct {
		name  string
		token string
		user  uuid.UUID
		now   time.Time
		want  Reason
	}{
		{"valid", tok, testUser, testNow, Valid},
		{"valid a second before expiry", tok, testUser, testNow.Add(TokenTTL - time.Second), Valid},
		{"missing", "", testUser, testNow, Missing},
		{"expired at exp", tok, testUser, testNow.Add(TokenTTL), Expired},
		{"expired later", tok, testUser, testNow.Add(2 * TokenTTL), Expired},
		{"other account", tok, otherUser, testNow, OtherAccount},
		{"over 512 characters", tok + strings.Repeat("A", maxTokenLen-len(tok)+1), testUser, testNow, Invalid},
		{"exactly 512 characters, not a token", strings.Repeat("A", maxTokenLen), testUser, testNow, Invalid},
		{"version v2", join("v2", parts[1], parts[2], parts[3]), testUser, testNow, Invalid},
		{"three parts", join(parts[0], parts[1], parts[2]), testUser, testNow, Invalid},
		{"five parts", tok + ".x", testUser, testNow, Invalid},
		{"unknown kid", tokOtherKid, testUser, testNow, Invalid},
		{"kid upper case", join(parts[0], strings.ToUpper("0000000a"), parts[2], parts[3]), testUser, testNow, Invalid},
		{"same kid, another key", tokForged, testUser, testNow, Invalid},
		{"body changed", join(parts[0], parts[1], flip(parts[2]), parts[3]), testUser, testNow, Invalid},
		{"mac changed", join(parts[0], parts[1], parts[2], flip(parts[3])), testUser, testNow, Invalid},
		{"mac with a line break", join(parts[0], parts[1], parts[2], parts[3][:20]+"\n"+parts[3][20:]), testUser, testNow, Invalid},
		{"mac padded", tok + "=", testUser, testNow, Invalid},
		{"mac empty", join(parts[0], parts[1], parts[2], ""), testUser, testNow, Invalid},
		{"issued 61 s ahead", future, testUser, testNow, Invalid},
		{"issued 60 s ahead", edge, testUser, testNow, Valid},
		{"issued 61 s ahead, for another account", future, otherUser, testNow, Invalid},
		{"expired, for another account", tok, otherUser, testNow.Add(TokenTTL), Expired},
	}
	for _, tc := range cases {
		if got := r.Check(tc.token, tc.user, tc.now); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if Missing != "missing" || Invalid != "invalid" || Expired != "expired" || OtherAccount != "other_account" || Valid != "" {
		t.Error("the reasons are the warn line's values (plan, D245)")
	}
}

// Rotation (D245): the first line signs, every line verifies.
func TestRingRotation(t *testing.T) {
	l1, l2, l3 := testRingLine(0x10, 0xaaaaaaa1), testRingLine(0x20, 0xaaaaaaa2), testRingLine(0x30, 0xaaaaaaa3)
	three := mustRing(t, l1, l2, l3)
	onlyTwo := mustRing(t, l2)
	byTwo, _ := onlyTwo.Mint(testUser, testKeyID(), testNow)
	if got := three.Check(byTwo, testUser, testNow); got != Valid {
		t.Fatalf("a token of line 2 under the 3-line ring: %q", got)
	}
	byThree, _ := three.Mint(testUser, testKeyID(), testNow)
	if !strings.HasPrefix(byThree, "v1.aaaaaaa1.") {
		t.Fatalf("the 3-line ring did not sign with line 1: %s", byThree[:12])
	}
	if got := onlyTwo.Check(byThree, testUser, testNow); got != Invalid {
		t.Fatalf("line 1's token under a ring without it: %q", got)
	}
	byLast, _ := mustRing(t, l3).Mint(testUser, testKeyID(), testNow)
	if got := three.Check(byLast, testUser, testNow); got != Valid {
		t.Fatalf("a token of line 3 under the 3-line ring: %q", got)
	}
	if got := mustRing(t, l1+"\n").Check(byThree, testUser, testNow); got != Valid {
		t.Fatalf("a trailing newline changed the ring: %q", got)
	}
}

func TestParseRingRefuses(t *testing.T) {
	l1, l2 := testRingLine(0x10, 0xbbbbbbb1), testRingLine(0x20, 0xbbbbbbb2)
	kid1, key1, _ := strings.Cut(l1, ":")
	short := base64.StdEncoding.EncodeToString(make([]byte, 31))
	long := base64.StdEncoding.EncodeToString(make([]byte, 33))
	cases := map[string]string{
		"empty":                 "",
		"only a newline":        "\n",
		"four lines":            strings.Join([]string{l1, l2, testRingLine(3, 3), testRingLine(4, 4)}, "\n"),
		"duplicate kid":         l1 + "\n" + kid1 + ":" + strings.SplitN(l2, ":", 2)[1],
		"same line twice":       l1 + "\n" + l1,
		"blank line between":    l1 + "\n\n" + l2,
		"two trailing newlines": l1 + "\n\n",
		"CRLF":                  l1 + "\r\n" + l2,
		"no colon":              kid1 + key1,
		"kid upper case":        strings.ToUpper(kid1) + ":" + key1,
		"kid 7 digits":          kid1[:7] + ":" + key1,
		"kid 9 digits":          kid1 + "0:" + key1,
		"kid not hex":           "bbbbbbbg:" + key1,
		"key 31 bytes":          kid1 + ":" + short,
		"key 33 bytes":          kid1 + ":" + long,
		"key base64url":         kid1 + ":" + strings.TrimRight(base64.URLEncoding.EncodeToString([]byte(strings.Repeat("\xfb", 32))), "="),
		"key unpadded":          kid1 + ":" + strings.TrimRight(key1, "="),
		"space after colon":     kid1 + ": " + key1,
		"trailing space":        l1 + " ",
		"leading space":         " " + l1,
	}
	for name, s := range cases {
		r, err := ParseRing(s)
		if err == nil || r != nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		// The error names the line, never its content.
		for _, secret := range []string{key1, strings.TrimRight(key1, "="), key1[:12], short, long} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error %q holds key material", name, err)
			}
		}
	}
	if _, err := ParseRing(l1 + "\n" + l2 + "\n"); err != nil {
		t.Fatalf("two lines and a trailing newline: %v", err)
	}
}
