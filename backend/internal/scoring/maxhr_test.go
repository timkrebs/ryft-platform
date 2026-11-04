package scoring

import "testing"

// The minimum age is 16 (D143): year − 16 is the youngest accepted birth year.
func TestValidBirthYear(t *testing.T) {
	for _, c := range []struct {
		birth int
		want  bool
	}{
		{1919, false}, {1920, true}, {2000, true},
		{2009, true}, {2010, true}, // 2026 − 16
		{2011, false}, {2013, false}, // 15 and 13 years old
		{2026, false},
	} {
		if got := ValidBirthYear(c.birth, 2026); got != c.want {
			t.Errorf("born %d, in 2026: %v, want %v", c.birth, got, c.want)
		}
	}
	if MinAge != 16 {
		t.Errorf("MinAge = %d, want 16", MinAge)
	}
}

// Pins Tanaka (208 − 0.7 · age) and its rounding, half up.
func TestEstimatedMaxHR(t *testing.T) {
	for _, c := range []struct{ birth, want int }{
		{1990, 183}, // 36: 182.8
		{1991, 184}, // 35: 183.5, half up
		{1996, 187}, // 30: exactly 187
		{2000, 190}, // 26: 189.8
		{2010, 197}, // 16, the youngest: 196.8
		{1960, 162}, // 66: 161.8
		{1920, 134}, // 106, the oldest: 133.8
	} {
		if got := EstimatedMaxHR(c.birth, 2026); got != c.want {
			t.Errorf("born %d, in 2026: %d, want %d", c.birth, got, c.want)
		}
	}
}
