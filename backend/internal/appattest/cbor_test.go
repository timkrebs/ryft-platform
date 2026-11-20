package appattest

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeCBORAccepts(t *testing.T) {
	cases := []struct {
		name string
		hex  string
		want cborValue
	}{
		{"uint 0", "00", cborValue{kind: cborUint, num: 0}},
		{"uint 23", "17", cborValue{kind: cborUint, num: 23}},
		{"uint 24", "1818", cborValue{kind: cborUint, num: 24}},
		{"uint 1000", "1903e8", cborValue{kind: cborUint, num: 1000}},
		{"uint 2^32-1", "1affffffff", cborValue{kind: cborUint, num: 1<<32 - 1}},
		{"uint 2^64-1", "1bffffffffffffffff", cborValue{kind: cborUint, num: 1<<64 - 1}},
		{"non-minimal uint", "1800", cborValue{kind: cborUint, num: 0}},
		{"neg -1", "20", cborValue{kind: cborNeg, num: 0}},
		{"neg -1000", "3903e7", cborValue{kind: cborNeg, num: 999}},
		{"bytes empty", "40", cborValue{kind: cborBytes, bytes: []byte{}}},
		{"bytes", "4401020304", cborValue{kind: cborBytes, bytes: []byte{1, 2, 3, 4}}},
		{"text", "6449455446", cborValue{kind: cborText, bytes: []byte("IETF")}},
		{"text utf-8", "62c3bc", cborValue{kind: cborText, bytes: []byte("ü")}},
		{"array", "83010203", cborValue{kind: cborArray, items: []cborValue{{kind: cborUint, num: 1}, {kind: cborUint, num: 2}, {kind: cborUint, num: 3}}}},
		{"map", "a2616101616202", cborValue{kind: cborMap, pairs: []cborPair{{"a", cborValue{kind: cborUint, num: 1}}, {"b", cborValue{kind: cborUint, num: 2}}}}},
		{"empty map", "a0", cborValue{kind: cborMap, pairs: []cborPair{}}},
	}
	for _, tc := range cases {
		got, err := decodeCBOR(mustHex(t, tc.hex))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !equalCBOR(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func equalCBOR(a, b cborValue) bool {
	if a.kind != b.kind || a.num != b.num || !bytes.Equal(a.bytes, b.bytes) || len(a.items) != len(b.items) || len(a.pairs) != len(b.pairs) {
		return false
	}
	for i := range a.items {
		if !equalCBOR(a.items[i], b.items[i]) {
			return false
		}
	}
	for i := range a.pairs {
		if a.pairs[i].key != b.pairs[i].key || !equalCBOR(a.pairs[i].val, b.pairs[i].val) {
			return false
		}
	}
	return true
}

func TestDecodeCBORRefuses(t *testing.T) {
	cases := map[string]string{
		"empty":                     "",
		"trailing byte":             "0000",
		"trailing after map":        "a0 00",
		"truncated argument":        "19 03",
		"truncated bytes":           "44 0102",
		"truncated array":           "83 0102",
		"truncated map":             "a1 6161",
		"bytes length beyond input": "5b ffffffffffffffff 00",
		"text length beyond input":  "7a ffffffff 00",
		"array count beyond input":  "9b ffffffffffffffff 00",
		"map count beyond input":    "bb 7fffffffffffffff 00",
		"indefinite bytes":          "5f 4101 ff",
		"indefinite text":           "7f 6161 ff",
		"indefinite array":          "9f 01 ff",
		"indefinite map":            "bf 616101 ff",
		"break alone":               "ff",
		"reserved 28":               "1c",
		"reserved 29":               "3d",
		"reserved 30":               "5e",
		"tag 0":                     "c0 74 323031332d30332d32315432303a30343a30305a",
		"tag 24":                    "d818 41 00",
		"false":                     "f4",
		"true":                      "f5",
		"null":                      "f6",
		"undefined":                 "f7",
		"half float":                "f9 3c00",
		"single float":              "fa 47c35000",
		"double float":              "fb 3ff199999999999a",
		"simple 24":                 "f8 20",
		"invalid utf-8":             "62 c328",
		"int map key":               "a1 01 02",
		"bytes map key":             "a1 4161 02",
		"duplicate map key":         "a2 616101 616102",
		"float inside an array":     "82 01 f9 3c00",
		"tag inside a map":          "a1 6161 c1 01",
	}
	for name, h := range cases {
		if _, err := decodeCBOR(mustHex(t, h)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDecodeCBORLimits(t *testing.T) {
	nested := func(n int) []byte { // n nested one-item arrays around 0
		return append(bytes.Repeat([]byte{0x81}, n), 0x00)
	}
	if _, err := decodeCBOR(nested(maxCBORDepth)); err != nil {
		t.Errorf("depth %d refused: %v", maxCBORDepth, err)
	}
	if _, err := decodeCBOR(nested(maxCBORDepth + 1)); err == nil {
		t.Errorf("depth %d accepted", maxCBORDepth+1)
	}
	mapsDeep := append(bytes.Repeat([]byte{0xa1, 0x61, 'k'}, maxCBORDepth+1), 0x00)
	if _, err := decodeCBOR(mapsDeep); err == nil {
		t.Error("maps nested too deep accepted")
	}
	deepThenTrailing := append(nested(1000), 0x00)
	if _, err := decodeCBOR(deepThenTrailing); err == nil {
		t.Error("1000 levels accepted")
	}

	// The size limit counts the whole input.
	atLimit := cborEncode(make([]byte, maxCBORSize-3)) // 0x59 + 2 length bytes
	if len(atLimit) != maxCBORSize {
		t.Fatalf("built %d bytes", len(atLimit))
	}
	if _, err := decodeCBOR(atLimit); err != nil {
		t.Errorf("%d bytes refused: %v", maxCBORSize, err)
	}
	if _, err := decodeCBOR(cborEncode(make([]byte, maxCBORSize-2))); err == nil {
		t.Errorf("%d bytes accepted", maxCBORSize+1)
	}
}

func TestCBORField(t *testing.T) {
	v, err := decodeCBOR(cborEncode(cmap{{"a", 1}, {"b", "x"}}))
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := v.field("b"); !ok || b.kind != cborText || string(b.bytes) != "x" {
		t.Errorf("field b: %+v %v", b, ok)
	}
	if _, ok := v.field("c"); ok {
		t.Error("missing field found")
	}
	if _, ok := (cborValue{kind: cborArray}).field("a"); ok {
		t.Error("field of an array found")
	}
}

// FuzzDecode: the decoder never panics, and what it accepts is within its limits.
func FuzzDecode(f *testing.F) {
	fx := newFixture(f)
	for _, seed := range [][]byte{
		fx.build(f, fx.defaults()),
		fx.assertion(f, fx.key, testAppID, 1, fx.cdh),
		{0xa0}, {0x9f, 0xff}, {0x5b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		append(bytes.Repeat([]byte{0x81}, 9), 0),
		{0xbb, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := decodeCBOR(data)
		if err != nil {
			return
		}
		if len(data) == 0 || len(data) > maxCBORSize {
			t.Fatalf("accepted %d bytes", len(data))
		}
		if d := depth(v); d > maxCBORDepth {
			t.Fatalf("accepted depth %d", d)
		}
	})
}

func depth(v cborValue) int {
	d := 0
	for _, it := range v.items {
		d = max(d, depth(it))
	}
	for _, p := range v.pairs {
		d = max(d, depth(p.val))
	}
	if v.kind == cborArray || v.kind == cborMap {
		return d + 1
	}
	return d
}
