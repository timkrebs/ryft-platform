package scoring

// DefaultMaxHR is max_hr while nothing is known about the user (bpm).
const DefaultMaxHR = 190

// The birth years PATCH /v1/me accepts: 1920 up to this year minus MinAge.
const (
	MinBirthYear = 1920
	MinAge       = 13
)

// EstimatedMaxHR is Tanaka's estimate, round(208 − 0.7 · age), with the age
// from the birth year alone (D81). Integer tenths, so the rounding (half up)
// cannot depend on how 0.7 is stored.
func EstimatedMaxHR(birthYear, year int) int {
	age := year - birthYear
	return (2080 - 7*age + 5) / 10
}
