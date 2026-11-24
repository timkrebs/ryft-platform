package importer

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
)

// MappingVersion pins the adapter rules used when resuming an import.
const MappingVersion = "1"

// Options controls record selection and validation. Limit bounds raw input rows.
type Options struct {
	Version         string
	Limit           int
	ChunkSize       int
	After           int64
	Markets         []string
	MinCompleteness int
	EnergyTolerance float64
}

// Result reports one source row and its normalized value when eligible.
type Result struct {
	Offset  int64
	Record  *nutrition.CatalogRecord
	Outcome string
	Reasons []string
}

// Emit handles one processed row and can stop the adapter with an error.
type Emit func(Result) error

type nutrientKey struct{ code, unit string }

var releaseLabel = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)

// ValidOptions checks the common adapter limits and policy settings.
func ValidOptions(o Options) bool {
	return releaseLabel.MatchString(o.Version) && o.Limit >= 1 && o.ChunkSize >= 1 && o.ChunkSize <= 2000 &&
		o.After >= 0 && o.After <= int64(o.Limit) && o.MinCompleteness >= 0 && o.MinCompleteness <= 100 &&
		o.EnergyTolerance >= 0.1 && o.EnergyTolerance <= 5
}

func parseAmount(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
}

func mapped(n nutrientKey, inputUnit string, amount, ref float64, base, method string) (model.FoodNutrient, error) {
	value, err := nutrition.NormalizeNutrient(model.FoodNutrientInput{
		Code: n.code, Unit: inputUnit, Amount: amount, ReferenceAmount: ref, ReferenceUnit: base,
	}, n.unit)
	return model.FoodNutrient{FoodNutrientInput: value, Method: method, Quality: "source_reported"}, err
}

func result(offset int64, r nutrition.CatalogRecord, warnings []string, o Options) Result {
	if !nutrition.Text(r.Name, 200) || nutrition.Normalize(r.Name) == "" ||
		!nutrition.Text(r.Brand, 160) || r.ExternalID == "" || !nutrition.Text(r.ExternalID, 200) ||
		r.Version == "" || r.License == "" || r.URL == "" {
		return Result{Offset: offset, Outcome: "rejected", Reasons: []string{"invalid_identity"}}
	}
	if r.Completeness() < o.MinCompleteness {
		return Result{Offset: offset, Outcome: "filtered", Reasons: []string{"completeness"}}
	}
	if len(r.Nutrients) == 0 {
		warnings = append(warnings, "nutrients_missing")
	}
	if suspectEnergy(r.Nutrients, o.EnergyTolerance) {
		warnings = append(warnings, "energy_plausibility")
	}
	status := "valid"
	if len(warnings) > 0 {
		status, r.Quality = "valid_with_warnings", "warnings"
	}
	slices.Sort(warnings)
	return Result{Offset: offset, Record: &r, Outcome: status, Reasons: slices.Compact(warnings)}
}

func suspectEnergy(values []model.FoodNutrient, threshold float64) bool {
	groups := map[string]map[string]float64{}
	for _, n := range values {
		key := n.ReferenceUnit + strconv.FormatFloat(n.ReferenceAmount, 'f', -1, 64)
		if groups[key] == nil {
			groups[key] = make(map[string]float64)
		}
		groups[key][n.Code] = n.Amount
	}
	for _, g := range groups {
		energy, haveEnergy := g["energy_kcal"]
		protein, haveProtein := g["protein"]
		carb, haveCarb := g["carbohydrates"]
		if !haveCarb {
			carb, haveCarb = g["carbohydrates_total"]
		}
		fat, haveFat := g["fat"]
		if haveEnergy && haveProtein && haveCarb && haveFat {
			estimate := protein*4 + carb*4 + fat*9
			if math.Abs(energy-estimate) > math.Max(15, math.Max(energy, estimate)*threshold) {
				return true
			}
		}
	}
	return false
}
