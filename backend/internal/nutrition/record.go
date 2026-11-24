package nutrition

import (
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// CatalogRecord is a normalized, source-owned food from an importer or a private creation.
type CatalogRecord struct {
	Name, Brand, Type, Quality string
	Markets                    []string
	Names                      []model.FoodName
	Portions                   []model.FoodPortionInput
	Nutrients                  []model.FoodNutrient
	Barcodes                   []Barcode
	Provider, ExternalID       string
	Version, License, URL      string
	SourceUpdatedAt            *time.Time
}

// Barcode records the source text, normalized identifier and market.
type Barcode struct {
	Original, GTIN, Market string
}

// Completeness is a ranking hint derived only from the common core fields.
func (r CatalogRecord) Completeness() int {
	core := map[string]bool{
		"protein": false, "fat": false, "carbohydrates": false, "energy_kcal": false, "fiber": false,
	}
	for _, n := range r.Nutrients {
		code := n.Code
		switch code {
		case "energy_kj":
			code = "energy_kcal"
		case "carbohydrates_total":
			code = "carbohydrates"
		}
		if _, ok := core[code]; ok {
			core[code] = true
		}
	}
	n := 0
	for _, present := range core {
		if present {
			n++
		}
	}
	return n * 20
}
