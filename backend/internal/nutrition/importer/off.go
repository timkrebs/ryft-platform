package importer

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
)

const offMaxRecord = 2 << 20

// These keys use the OFF normalized representation: g for mass and explicit
// kcal/kJ for energy. Vitamin and alcohol fields with uncertain equivalents are left out.
var offNutrients = map[string]nutrientKey{
	"energy-kcal": {"energy_kcal", "kcal"}, "energy-kj": {"energy_kj", "kJ"},
	"proteins": {"protein", "g"}, "fat": {"fat", "g"}, "carbohydrates": {"carbohydrates", "g"},
	"carbohydrates-total": {"carbohydrates_total", "g"}, "fiber": {"fiber", "g"}, "sugars": {"total_sugar", "g"},
	"added-sugars": {"added_sugar", "g"}, "saturated-fat": {"saturated_fat", "g"},
	"monounsaturated-fat": {"monounsaturated_fat", "g"}, "polyunsaturated-fat": {"polyunsaturated_fat", "g"},
	"trans-fat": {"trans_fat", "g"}, "omega-3-fat": {"omega_3", "g"}, "omega-6-fat": {"omega_6", "g"},
	"eicosapentaenoic-acid": {"epa", "g"}, "docosahexaenoic-acid": {"dha", "g"},
	"sodium": {"sodium", "mg"}, "potassium": {"potassium", "mg"}, "calcium": {"calcium", "mg"},
	"magnesium": {"magnesium", "mg"}, "iron": {"iron", "mg"}, "zinc": {"zinc", "mg"},
	"phosphorus": {"phosphorus", "mg"}, "selenium": {"selenium", "ug"},
	"vitamin-c": {"vitamin_c", "mg"}, "vitamin-d": {"vitamin_d", "ug"}, "vitamin-b1": {"vitamin_b1", "mg"},
	"vitamin-b2": {"vitamin_b2", "mg"}, "vitamin-b6": {"vitamin_b6", "mg"}, "vitamin-b12": {"vitamin_b12", "ug"},
	"caffeine": {"caffeine", "mg"}, "water": {"water", "g"}, "cholesterol": {"cholesterol", "mg"},
}

var marketTags = map[string]string{
	"DE": "en:germany", "AT": "en:austria", "CH": "en:switzerland", "FR": "en:france", "BE": "en:belgium",
	"NL": "en:netherlands", "LU": "en:luxembourg", "IT": "en:italy", "ES": "en:spain", "PT": "en:portugal",
	"DK": "en:denmark", "CZ": "en:czech-republic", "PL": "en:poland", "SE": "en:sweden", "FI": "en:finland",
	"IE": "en:ireland", "GB": "en:united-kingdom", "NO": "en:norway",
}

// ValidOFFMarket checks that the adapter knows the provider tag for a country.
func ValidOFFMarket(s string) bool { return marketTags[s] != "" }

type offProduct struct {
	Code            string                     `json:"code"`
	Name            string                     `json:"product_name"`
	DE              string                     `json:"product_name_de"`
	EN              string                     `json:"product_name_en"`
	Language        string                     `json:"lang"`
	LC              string                     `json:"lc"`
	Brands          string                     `json:"brands"`
	Countries       []string                   `json:"countries_tags"`
	Basis           string                     `json:"nutrition_data_per"`
	NoNutrition     string                     `json:"no_nutrition_data"`
	ServingQuantity json.RawMessage            `json:"serving_quantity"`
	ServingUnit     string                     `json:"serving_quantity_unit"`
	ProductUnit     string                     `json:"product_quantity_unit"`
	Modified        json.RawMessage            `json:"last_modified_t"`
	Nutrients       map[string]json.RawMessage `json:"nutriments"`
}

// OFF reads the official JSONL product shape, optionally gzip-compressed.
func OFF(ctx context.Context, input io.Reader, o Options, emit Emit) error {
	if !ValidOptions(o) || len(o.Markets) == 0 || slices.ContainsFunc(o.Markets, func(m string) bool { return !ValidOFFMarket(m) }) {
		return nutrition.ErrInvalid
	}
	b := bufio.NewReader(input)
	var reader io.Reader = b
	signature, _ := b.Peek(2)
	if len(signature) == 2 && signature[0] == 0x1f && signature[1] == 0x8b {
		m, err := gzip.NewReader(b)
		if err != nil {
			return errors.New("invalid OFF gzip header")
		}
		defer m.Close()
		reader = m
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), offMaxRecord)
	for offset := int64(1); offset <= int64(o.Limit) && scanner.Scan(); offset++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if offset <= o.After {
			continue
		}
		var item offProduct
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			if err := emit(Result{Offset: offset, Outcome: "rejected", Reasons: []string{"off_record_shape"}}); err != nil {
				return err
			}
			continue
		}
		if err := emit(offRecord(item, offset, o)); err != nil {
			return err
		}
	}
	if scanner.Err() != nil {
		return errors.New("OFF stream is truncated or a record exceeds 2 MiB")
	}
	return nil
}

func offRecord(item offProduct, offset int64, o Options) Result {
	var markets []string
	for market, tag := range marketTags {
		if slices.Contains(item.Countries, tag) {
			markets = append(markets, market)
		}
	}
	slices.Sort(markets)
	if !slices.ContainsFunc(markets, func(m string) bool { return slices.Contains(o.Markets, m) }) {
		return Result{Offset: offset, Outcome: "filtered", Reasons: []string{"market"}}
	}
	gtin, err := nutrition.GTIN(item.Code)
	if err != nil {
		return Result{Offset: offset, Outcome: "quarantined", Reasons: []string{"invalid_gtin"}}
	}
	name := strings.TrimSpace(item.Name)
	lang := item.LC
	if !nutrition.Language(lang) {
		lang = item.Language
	}
	if !nutrition.Language(lang) {
		lang = "und"
	}
	names := []model.FoodName{}
	for _, n := range []model.FoodName{{Language: "de", Label: strings.TrimSpace(item.DE)}, {Language: "en", Label: strings.TrimSpace(item.EN)}, {Language: lang, Label: name}} {
		if nutrition.ValidName(n) && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if !nutrition.ValidName(model.FoodName{Language: lang, Label: name}) && len(names) > 0 {
		name = names[0].Label
	}
	brand, _, _ := strings.Cut(item.Brands, ",")
	r := nutrition.CatalogRecord{
		Name: name, Brand: strings.TrimSpace(brand), Markets: markets, Names: names, Type: "branded", Quality: "source_reported",
		Provider: "off", ExternalID: item.Code, Version: o.Version, License: "ODbL-1.0",
		URL: "https://world.openfoodfacts.org/product/" + item.Code,
	}
	if mod, err := strconv.ParseInt(rawNumber(item.Modified), 10, 64); err == nil && mod > 0 && mod < 7258118400 {
		at := time.Unix(mod, 0).UTC()
		r.SourceUpdatedAt = &at
	}
	for _, m := range markets {
		r.Barcodes = append(r.Barcodes, nutrition.Barcode{Original: item.Code, GTIN: gtin, Market: m})
	}
	var warnings []string
	offPortion(&r, item)
	if item.NoNutrition != "on" {
		r.Nutrients, warnings = offValues(item)
	}
	return result(offset, r, warnings, o)
}

func offPortion(r *nutrition.CatalogRecord, p offProduct) {
	size, ok := parseAmount(rawNumber(p.ServingQuantity))
	portion := model.FoodPortionInput{Label: "Serving", Unit: "serving", Quantity: 1,
		Labels: map[string]string{"en": "Serving", "de": "Portion"}}
	if ok && nutrition.Quantity(size) {
		switch p.ServingUnit {
		case "g":
			portion.WeightG = &size
		case "ml":
			portion.VolumeML = &size
		}
	}
	if portion.WeightG != nil || portion.VolumeML != nil || p.Basis == "serving" {
		r.Portions = append(r.Portions, portion)
	}
}

func offValues(p offProduct) ([]model.FoodNutrient, []string) {
	if len(p.Nutrients) == 0 {
		return nil, nil
	}
	base, suffix := "g", "_100g"
	ref := 100.0
	switch p.Basis {
	case "100g":
		// OFF uses the 100g key for both mass and volume. Do not assume mass
		// when a volumetric serving or package makes the label basis ambiguous.
		if p.ServingUnit == "ml" || p.ProductUnit == "ml" || p.ProductUnit == "l" {
			return nil, []string{"ambiguous_100g_ml"}
		}
	case "100ml":
		base = "ml"
	case "serving":
		base, suffix, ref = "serving", "_serving", 1
	default:
		return nil, []string{"unknown_nutrition_basis"}
	}
	keys := make([]string, 0, len(offNutrients))
	for key := range offNutrients {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var values []model.FoodNutrient
	invalid := false
	for _, key := range keys {
		raw := rawNumber(p.Nutrients[key+suffix])
		if raw == "" {
			continue
		}
		amount, ok := parseAmount(raw)
		n := offNutrients[key]
		unit := "g"
		if key == "energy-kcal" || key == "energy-kj" {
			unit = n.unit
		}
		value, err := mapped(n, unit, amount, ref, base, "off:"+key+suffix)
		if !ok || err != nil {
			invalid = true
			continue
		}
		values = append(values, value)
	}
	if invalid {
		return values, []string{"invalid_nutrient"}
	}
	return values, nil
}

func rawNumber(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	return string(raw)
}
