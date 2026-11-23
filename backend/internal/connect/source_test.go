package connect

import "testing"

func TestSourceNames(t *testing.T) {
	if got := SourceName("garmin"); got != "ryft-direct-garmin" {
		t.Fatalf("SourceName = %q", got)
	}
	for name, want := range map[string]bool{"ryft-direct-garmin": true, "ryft-direct-": true, "ryft-direct": false, "Garmin": false, "": false, "my ryft-direct-x": false} {
		if got := IsReservedSource(name); got != want {
			t.Errorf("IsReservedSource(%q) = %v", name, got)
		}
	}
	if p, ok := ProviderOfSource("ryft-direct-suunto"); !ok || p != "suunto" {
		t.Errorf("ProviderOfSource = %q %v", p, ok)
	}
	for _, name := range []string{"ryft-direct-", "ryft-direct-A", "Garmin Connect", "ryft-direct-a"} {
		if p, ok := ProviderOfSource(name); ok {
			t.Errorf("ProviderOfSource(%q) = %q, want none", name, p)
		}
	}
}

func TestValidProvider(t *testing.T) {
	for id, want := range map[string]bool{"garmin": true, "fake": true, "a1": true, "a": false, "1a": false, "Garmin": false, "gar-min": false,
		"abcdefghijklmnopqrstuvwxyzabcdef": true, "abcdefghijklmnopqrstuvwxyzabcdefg": false, "": false} {
		if got := ValidProvider(id); got != want {
			t.Errorf("ValidProvider(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestVendorOfBundle(t *testing.T) {
	if got := VendorOfBundle("com.garmin.connect.mobile"); got != "garmin" {
		t.Errorf("garmin bundle = %q", got)
	}
	for _, b := range []string{"", "com.apple.health", "com.garmin.connect.mobile.beta"} {
		if got := VendorOfBundle(b); got != "" {
			t.Errorf("VendorOfBundle(%q) = %q", b, got)
		}
	}
}

func TestRecordKey(t *testing.T) {
	if got := RecordKey("sleep", "n-1"); got != "sleep:n-1" {
		t.Fatal(got)
	}
}
