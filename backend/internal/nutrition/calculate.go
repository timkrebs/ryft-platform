package nutrition

import (
	"errors"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // API containers need the same IANA database on every host.

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// CalculationVersion identifies the portion rules used for stored snapshots.
const CalculationVersion = 1

// ErrConversion reports that the food cannot be measured on the requested basis.
var ErrConversion = errors.New("food has no compatible nutrient reference for the requested measure")

// LocalDate converts an instant using the event's captured IANA timezone.
func LocalDate(at time.Time, zone string) (string, error) {
	if at.IsZero() || at.Year() < 1900 || at.Year() > 2200 || !Text(zone, 100) ||
		(zone != "UTC" && !strings.Contains(zone, "/")) ||
		strings.HasPrefix(zone, "posix/") || strings.HasPrefix(zone, "right/") {
		return "", ErrInvalid
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return "", ErrInvalid
	}
	return at.In(loc).Format(time.DateOnly), nil
}

// ValidateMeal validates meal fields and resolves the captured local day.
func ValidateMeal(m model.NutritionMealInput) (string, error) {
	day, err := LocalDate(m.ConsumedAt, m.Timezone)
	if err != nil || !Text(m.Name, 200) || !Text(m.Notes, 2000) ||
		!slices.Contains([]string{"breakfast", "lunch", "dinner", "snack", "other"}, m.MealType) ||
		!slices.Contains([]string{"manual_search", "barcode", "photo_capture", "photo_import", "natural_language",
			"nutrition_label", "external_import", "manual_entry"}, m.InputMethod) {
		return "", ErrInvalid
	}
	return day, nil
}

func decimal(n float64) *big.Rat {
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(n, 'g', -1, 64))
	return r
}

func round(n float64, precision int) float64 {
	return decimalRatio(n, 1, 1, precision)
}

func decimalRatio(n, mul, div float64, precision int) float64 {
	r := decimal(n)
	r.Mul(r, decimal(mul))
	r.Quo(r, decimal(div))
	v, _ := strconv.ParseFloat(r.FloatString(precision), 64)
	return v
}

func qualityRank(quality string) int {
	switch quality {
	case "verified":
		return 3
	case "source_reported":
		return 2
	case "user_reported":
		return 1
	default:
		return 0
	}
}

type measures struct {
	g, ml, servings float64
	preferred       string
	portionSource   uuid.UUID
}

func resolve(food model.FoodDetail, in model.NutritionMealItemInput) (measures, error) {
	m := measures{preferred: in.Unit}
	if !Quantity(in.Quantity) || in.FoodID != food.ID {
		return m, ErrInvalid
	}
	switch in.Unit {
	case "g", "ml":
		if in.Portion != nil {
			return m, ErrInvalid
		}
		if in.Unit == "g" {
			m.g = in.Quantity
		} else {
			m.ml = in.Quantity
		}
	case "serving", "portion":
		if in.Portion == nil {
			return m, ErrInvalid
		}
		i := slices.IndexFunc(food.Portions, func(p model.FoodPortion) bool { return p.ID == *in.Portion })
		if i < 0 || food.Portions[i].Unit != in.Unit {
			return m, ErrInvalid
		}
		p := food.Portions[i]
		if p.WeightG != nil {
			m.g = decimalRatio(*p.WeightG, in.Quantity, p.Quantity, 6)
		}
		if p.VolumeML != nil {
			m.ml = decimalRatio(*p.VolumeML, in.Quantity, p.Quantity, 6)
		}
		if p.Unit == "serving" {
			m.servings, m.portionSource = in.Quantity, p.SourceID
		}
	default:
		return m, ErrInvalid
	}
	if d := food.Density; d != nil {
		if m.g == 0 && m.ml > 0 {
			m.g = decimalRatio(m.ml, *d, 1, 6)
		}
		if m.ml == 0 && m.g > 0 {
			m.ml = decimalRatio(m.g, 1, *d, 6)
		}
	}
	if m.g > 100000 || m.ml > 100000 {
		return m, ErrInvalid
	}
	if m.g == 0 && m.ml == 0 && m.servings == 0 {
		return m, ErrConversion
	}
	return m, nil
}

func (m measures) quantity(n model.FoodNutrient) float64 {
	switch n.ReferenceUnit {
	case "g":
		return m.g
	case "ml":
		return m.ml
	case "serving":
		if n.SourceID == m.portionSource {
			return m.servings
		}
	}
	return 0
}

func (m measures) priority(unit string) int {
	if unit == m.preferred {
		return 3
	}
	if unit == "g" {
		return 2
	}
	return 1
}

func betterReference(a, b model.FoodNutrient, m measures) bool {
	if ar, br := qualityRank(a.Quality), qualityRank(b.Quality); ar != br {
		return ar > br
	}
	if ap, bp := m.priority(a.ReferenceUnit), m.priority(b.ReferenceUnit); ap != bp {
		return ap > bp
	}
	if a.SourceID != b.SourceID {
		return a.SourceID.String() < b.SourceID.String()
	}
	return a.ReferenceAmount < b.ReferenceAmount
}

// Calculate selects one compatible reference per nutrient and rounds each quantity
// to six decimal places, half away from zero. Missing codes stay absent.
func Calculate(food model.FoodDetail, in model.NutritionMealItemInput) (model.NutritionMealItem, error) {
	m, err := resolve(food, in)
	if err != nil {
		return model.NutritionMealItem{}, err
	}
	chosen := make(map[string]model.FoodNutrient)
	for _, n := range food.Nutrients {
		if m.quantity(n) <= 0 {
			continue
		}
		if old, ok := chosen[n.Code]; !ok || betterReference(n, old, m) {
			chosen[n.Code] = n
		}
	}
	if len(chosen) == 0 && len(food.Nutrients) > 0 {
		return model.NutritionMealItem{}, ErrConversion
	}
	id := food.ID
	item := model.NutritionMealItem{
		FoodID: &id, FoodName: food.Name, Brand: food.Brand, Quantity: in.Quantity, Unit: in.Unit, Portion: in.Portion,
		Nutrients: make(map[string]model.NutritionSnapValue, len(chosen)), Sources: []model.FoodSource{},
		CalculationVersion: CalculationVersion,
	}
	if m.g > 0 {
		item.WeightG = &m.g
	}
	if m.ml > 0 {
		item.VolumeML = &m.ml
	}
	used := make(map[uuid.UUID]bool)
	for code, n := range chosen {
		amount := n.Amount
		if n.Unit != "index" {
			amount = decimalRatio(n.Amount, m.quantity(n), n.ReferenceAmount, 6)
		}
		item.Nutrients[code] = model.NutritionSnapValue{
			Amount: amount,
			Unit:   n.Unit, SourceID: n.SourceID, ReferenceAmount: n.ReferenceAmount, ReferenceUnit: n.ReferenceUnit,
			Method: n.Method, Quality: n.Quality,
		}
		used[n.SourceID] = true
	}
	if m.portionSource != uuid.Nil {
		used[m.portionSource] = true
	}
	if in.Portion != nil {
		i := slices.IndexFunc(food.Portions, func(p model.FoodPortion) bool { return p.ID == *in.Portion })
		used[food.Portions[i].SourceID] = true
	}
	for _, source := range food.Sources {
		if used[source.ID] || len(chosen) == 0 {
			item.Sources = append(item.Sources, source)
		}
	}
	return item, nil
}
