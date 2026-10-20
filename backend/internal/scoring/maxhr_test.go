package scoring

import "testing"

// Pins Tanaka (208 − 0.7 · age) and its rounding, half up.
func TestEstimatedMaxHR(t *testing.T) {
	for _, c := range []struct{ birth, want int }{
		{1990, 183}, // 36: 182.8
		{1991, 184}, // 35: 183.5, half up
		{1996, 187}, // 30: exactly 187
		{2000, 190}, // 26: 189.8
		{2013, 199}, // 13, the youngest: 198.9
		{1960, 162}, // 66: 161.8
		{1920, 134}, // 106, the oldest: 133.8
	} {
		if got := EstimatedMaxHR(c.birth, 2026); got != c.want {
			t.Errorf("born %d, in 2026: %d, want %d", c.birth, got, c.want)
		}
	}
}
