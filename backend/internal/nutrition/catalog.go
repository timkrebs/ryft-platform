package nutrition

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/timkrebs/ryft/internal/model"
)

// ErrInvalid identifies safe, payload-free validation errors.
var ErrInvalid = errors.New("invalid nutrition input")

var (
	languagePattern = regexp.MustCompile(`^[a-z]{2,8}(-[a-z0-9]{2,8})*$`)
	marketPattern   = regexp.MustCompile(`^[A-Z]{2}$`)
)

// Normalize produces a Unicode search key with accent, punctuation and case folding.
func Normalize(s string) string {
	var b strings.Builder
	space := false
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.IsMark(r):
			continue
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			if r == 'ß' {
				b.WriteString("ss")
			} else {
				b.WriteRune(r)
			}
		default:
			space = true
		}
	}
	return b.String()
}

// Market accepts an optional uppercase country code.
func Market(s string) bool { return s == "" || marketPattern.MatchString(s) }

// Language accepts a bounded lowercase language tag.
func Language(s string) bool { return len(s) <= 35 && languagePattern.MatchString(s) }

// Text checks that text can be stored in PostgreSQL within a character limit.
func Text(s string, limit int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= limit
}

// GTIN validates the GS1 check digit and left-pads an EAN-8, UPC-A, EAN-13 or GTIN-14.
func GTIN(s string) (string, error) {
	switch len(s) {
	case 8, 12, 13, 14:
	default:
		return "", ErrInvalid
	}
	sum := 0
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return "", ErrInvalid
		}
		n := int(s[i] - '0')
		if (len(s)-i)%2 == 0 {
			n *= 3
		}
		sum += n
	}
	if sum%10 != 0 || strings.Trim(s, "0") == "" {
		return "", ErrInvalid
	}
	return strings.Repeat("0", 14-len(s)) + s, nil
}

// ValidName checks that a localized label can be indexed.
func ValidName(n model.FoodName) bool {
	return Language(n.Language) && Text(n.Label, 200) && Normalize(n.Label) != ""
}

// ValidPortion checks dimensions, labels and conversion rules for a portion.
func ValidPortion(p model.FoodPortionInput) bool {
	if !Text(p.Label, 200) || strings.TrimSpace(p.Label) == "" || !Quantity(p.Quantity) ||
		!slices.Contains([]string{"serving", "portion"}, p.Unit) || len(p.Labels) > 15 {
		return false
	}
	for lang, label := range p.Labels {
		if !Language(lang) || !Text(label, 200) || strings.TrimSpace(label) == "" {
			return false
		}
	}
	return (p.WeightG == nil || Quantity(*p.WeightG)) &&
		(p.VolumeML == nil || Quantity(*p.VolumeML)) &&
		(p.WeightG != nil || p.VolumeML != nil || p.Unit == "serving")
}

// Quantity checks positive quantities representable with six decimal places.
func Quantity(v float64) bool {
	return v >= 0.000001 && v <= 100000 && round(v, 6) == v
}

// Unit converts equivalent mass units or accepts the same energy unit.
// An energy representation and chemically distinct compounds have separate codes.
func Unit(amount float64, from, to string) (float64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return 0, ErrInvalid
	}
	positive := amount > 0
	from, to = strings.ToLower(strings.TrimSpace(from)), strings.ToLower(to)
	from = strings.NewReplacer("µg", "ug", "μg", "ug", "mcg", "ug").Replace(from)
	mass := map[string]float64{"g": 1, "mg": 1000, "ug": 1000000}
	if from != to {
		if mass[from] == 0 || mass[to] == 0 {
			return 0, ErrInvalid
		}
		amount = decimalRatio(amount, mass[to], mass[from], 9)
	}
	if amount > 100000000 {
		return 0, ErrInvalid
	}
	converted := round(amount, 9)
	if converted == 0 && positive {
		return 0, ErrInvalid
	}
	return converted, nil
}

// NormalizeNutrient validates a reference basis and normalizes its canonical unit.
func NormalizeNutrient(n model.FoodNutrientInput, unit string) (model.FoodNutrientInput, error) {
	if !Quantity(n.ReferenceAmount) || !slices.Contains([]string{"g", "ml", "serving"}, n.ReferenceUnit) || unit == "" {
		return n, ErrInvalid
	}
	amount, err := Unit(n.Amount, n.Unit, unit)
	if err != nil {
		return n, err
	}
	if n.ReferenceUnit == "g" {
		// Allow normal label and analytical rounding. Energy is checked with
		// a physical upper bound, independent of the 4-4-9 warning heuristic.
		limits := map[string]float64{"g": 1.02, "mg": 1020, "ug": 1020000, "kcal": 10, "kJ": 42}
		if amount > n.ReferenceAmount*limits[unit] {
			return n, ErrInvalid
		}
	}
	n.Amount, n.Unit = amount, unit
	if unit == "index" && (amount > 150 || n.Code != "glycemic_index") {
		return n, ErrInvalid
	}
	return n, nil
}

// ValidQualityProfile validates explicit edible-mass fractions in a private food submission.
func ValidQualityProfile(p *model.NutritionQualityProfile) bool {
	if p == nil {
		return true
	}
	mass := 0.0
	for _, v := range []*float64{p.NonStarchyVegetableFraction, p.WholeFruitFraction,
		p.NutsLegumesFraction, p.RedProcessedMeatFraction} {
		if v != nil {
			if !(*v >= 0 && *v <= 1) {
				return false
			}
			mass += *v
		}
	}
	// Compare decimal values at the same precision used for stored food measures.
	return round(mass, 6) <= 1 && (p.SugaryDrink == nil || !*p.SugaryDrink ||
		p.WholeFruitFraction == nil || *p.WholeFruitFraction == 0)
}
