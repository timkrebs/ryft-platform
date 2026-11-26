package model

import (
	"time"

	"github.com/google/uuid"
)

const (
	// FlagNutrition preserves the protected pilot with optional scoring and coaching.
	FlagNutrition = "nutrition.enabled"
	// FlagNutritionDiary permits core diary reads and writes without advanced processing.
	FlagNutritionDiary = "nutrition.diary_enabled"
)

// FoodSummary is a catalog match. Nutrient profiles are fetched by ID.
type FoodSummary struct {
	ID               uuid.UUID             `json:"id"`
	Name             string                `json:"name"`
	Brand            string                `json:"brand"`
	Type             string                `json:"type"`
	Quality          string                `json:"quality"`
	Subtitle         string                `json:"subtitle,omitempty"`
	DefaultPortion   *FoodPortionSelection `json:"default_portion,omitempty"`
	LastUsedPortion  *FoodPortionSelection `json:"last_used_portion,omitempty"`
	ReferencePortion *FoodPortionSelection `json:"reference_portion,omitempty"`
	Favorited        *bool                 `json:"favorited,omitempty"`
}

// FoodName is a localized name or alias used by catalog search.
type FoodName struct {
	Language string `json:"language"`
	Label    string `json:"label"`
	Alias    bool   `json:"alias"`
}

// FoodSource identifies the provider, release and license for a food record.
type FoodSource struct {
	ID              uuid.UUID  `json:"id"`
	Provider        string     `json:"provider"`
	ExternalID      string     `json:"external_id"`
	DatasetVersion  string     `json:"dataset_version"`
	URL             string     `json:"source_url"`
	License         string     `json:"license_identifier"`
	Attribution     string     `json:"attribution,omitempty"`
	SourceUpdatedAt *time.Time `json:"source_updated_at,omitempty"`
}

// FoodPortionInput defines a labeled serving or another measured portion.
type FoodPortionInput struct {
	Label     string            `json:"label"`
	Labels    map[string]string `json:"labels,omitempty"`
	Quantity  float64           `json:"quantity"`
	Unit      string            `json:"unit"`
	WeightG   *float64          `json:"weight_g,omitempty"`
	VolumeML  *float64          `json:"volume_ml,omitempty"`
	Estimated bool              `json:"estimated"`
}

// FoodPortion is a saved portion with a stable ID and provenance.
type FoodPortion struct {
	FoodPortionInput
	ID           uuid.UUID `json:"id"`
	SourceID     uuid.UUID `json:"source_id"`
	PortionKey   string    `json:"portion_key,omitempty"`
	ShortLabelEN string    `json:"short_label_en,omitempty"`
	ShortLabelDE string    `json:"short_label_de,omitempty"`
	IsDefault    bool      `json:"is_default"`
	SortOrder    int       `json:"sort_order"`
}

// FoodPortionSelection is a compatible quantity and its calculated nutrition preview.
type FoodPortionSelection struct {
	PortionID  *uuid.UUID `json:"portion_id,omitempty"`
	Quantity   float64    `json:"quantity"`
	Unit       string     `json:"unit"`
	Label      string     `json:"label,omitempty"`
	ShortLabel string     `json:"short_label,omitempty"`
	GramWeight *float64   `json:"gram_weight,omitempty"`
	VolumeML   *float64   `json:"volume_ml,omitempty"`
	Kcal       *float64   `json:"kcal,omitempty"`
	ProteinG   *float64   `json:"protein_g,omitempty"`
	CarbsG     *float64   `json:"carbs_g,omitempty"`
	FatG       *float64   `json:"fat_g,omitempty"`
}

// FoodNutrientInput is an amount on an explicit mass, volume or serving basis.
type FoodNutrientInput struct {
	Code            string  `json:"code"`
	Amount          float64 `json:"amount"`
	Unit            string  `json:"unit"`
	ReferenceAmount float64 `json:"reference_amount"`
	ReferenceUnit   string  `json:"reference_unit"`
}

// FoodNutrient adds measurement provenance to a canonical nutrient value.
type FoodNutrient struct {
	FoodNutrientInput
	SourceID uuid.UUID `json:"source_id"`
	Method   string    `json:"method"`
	Quality  string    `json:"quality"`
}

// FoodDetail contains catalog names, values, portions and their sources.
type FoodDetail struct {
	FoodSummary
	QualityProfile    *NutritionQualityProfile `json:"quality_profile,omitempty"`
	Density           *float64                 `json:"density_g_ml,omitempty"`
	Names             []FoodName               `json:"names"`
	Sources           []FoodSource             `json:"sources"`
	Portions          []FoodPortion            `json:"portions"`
	Nutrients         []FoodNutrient           `json:"nutrients"`
	LastUsedSelection *FoodPortionSelection    `json:"last_used_selection,omitempty"`
}

// FoodSearch bounds and localizes a lexical catalog query.
type FoodSearch struct {
	Query    string `json:"query"`
	Language string `json:"language,omitempty"`
	Market   string `json:"market,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Offset   int    `json:"offset,omitempty"`
}

// FoodSearchPage contains ranked matches and an optional next offset.
type FoodSearchPage struct {
	Foods      []FoodSummary `json:"foods"`
	NextOffset *int          `json:"next_offset"`
}

// FoodBarcodeQuery looks up a GTIN string with an optional market preference.
type FoodBarcodeQuery struct {
	Barcode string `json:"barcode"`
	Market  string `json:"market,omitempty"`
}

// FoodBarcodeResult reports the normalized code and separate matching foods.
type FoodBarcodeResult struct {
	GTIN       string        `json:"gtin"`
	Matches    []FoodSummary `json:"matches"`
	Conflicted bool          `json:"conflicted"`
	Truncated  bool          `json:"truncated"`
}

// FoodNutrientWrite requires an explicit amount, including when it is zero.
type FoodNutrientWrite struct {
	Code            string   `json:"code"`
	Amount          *float64 `json:"amount"`
	Unit            string   `json:"unit"`
	ReferenceAmount float64  `json:"reference_amount"`
	ReferenceUnit   string   `json:"reference_unit"`
}

// FoodCustomInput creates an owner-private food with an idempotent client key.
type FoodCustomInput struct {
	QualityProfile  *NutritionQualityProfile `json:"quality_profile,omitempty"`
	ClientRequestID uuid.UUID                `json:"client_request_id"`
	Name            string                   `json:"name"`
	Brand           string                   `json:"brand,omitempty"`
	Names           []FoodName               `json:"names,omitempty"`
	Nutrients       []FoodNutrientWrite      `json:"nutrients,omitempty"`
	Portions        []FoodPortionInput       `json:"portions,omitempty"`
}

// FoodSubmissionInput records or submits an owner-private moderation draft.
type FoodSubmissionInput struct {
	FoodID uuid.UUID `json:"food_id"`
	Status string    `json:"status"`
}

// FoodSubmission is the saved moderation state of a private food.
type FoodSubmission struct {
	ID        uuid.UUID `json:"id"`
	FoodID    uuid.UUID `json:"food_id"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NutritionMealItemInput selects a portion or preserves a saved item by ID.
type NutritionMealItemInput struct {
	ItemID   *uuid.UUID `json:"item_id,omitempty"`
	FoodID   uuid.UUID  `json:"food_id,omitempty"`
	Quantity float64    `json:"quantity,omitempty"`
	Unit     string     `json:"unit,omitempty"`
	Portion  *uuid.UUID `json:"portion_id,omitempty"`
	Language string     `json:"language,omitempty"`
}

// NutritionMealInput creates a meal from reviewed food selections.
type NutritionMealInput struct {
	ClientRequestID uuid.UUID                `json:"client_request_id"`
	ConsumedAt      time.Time                `json:"consumed_at"`
	Timezone        string                   `json:"timezone"`
	MealType        string                   `json:"meal_type"`
	InputMethod     string                   `json:"input_method"`
	Name            string                   `json:"name,omitempty"`
	Notes           string                   `json:"notes,omitempty"`
	Items           []NutritionMealItemInput `json:"items"`
}

// NutritionMealPatch applies optional edits to an expected meal revision.
type NutritionMealPatch struct {
	Revision    int                       `json:"revision"`
	ConsumedAt  *time.Time                `json:"consumed_at,omitempty"`
	Timezone    *string                   `json:"timezone,omitempty"`
	MealType    *string                   `json:"meal_type,omitempty"`
	InputMethod *string                   `json:"input_method,omitempty"`
	Name        *string                   `json:"name,omitempty"`
	Notes       *string                   `json:"notes,omitempty"`
	Items       *[]NutritionMealItemInput `json:"items,omitempty"`
}

// NutritionSnapValue preserves the calculated amount and the reference used.
type NutritionSnapValue struct {
	Amount          float64   `json:"amount"`
	Unit            string    `json:"unit"`
	SourceID        uuid.UUID `json:"source_id"`
	ReferenceAmount float64   `json:"reference_amount"`
	ReferenceUnit   string    `json:"reference_unit"`
	Method          string    `json:"method"`
	Quality         string    `json:"quality"`
}

// NutritionMealItem preserves the food and nutrient snapshot from logging.
type NutritionMealItem struct {
	QualityProfile     *NutritionQualityProfile      `json:"quality_profile,omitempty"`
	ID                 uuid.UUID                     `json:"id"`
	FoodID             *uuid.UUID                    `json:"food_id"`
	FoodName           string                        `json:"food_name"`
	Brand              string                        `json:"brand"`
	Quantity           float64                       `json:"quantity"`
	Unit               string                        `json:"unit"`
	Portion            *uuid.UUID                    `json:"portion_id,omitempty"`
	PortionLabel       *string                       `json:"portion_label,omitempty"`
	PortionShortLabel  *string                       `json:"portion_short_label,omitempty"`
	WeightG            *float64                      `json:"weight_g"`
	VolumeML           *float64                      `json:"volume_ml"`
	Nutrients          map[string]NutritionSnapValue `json:"nutrients"`
	Sources            []FoodSource                  `json:"sources"`
	CalculationVersion int                           `json:"calculation_version"`
	CreatedAt          time.Time                     `json:"created_at"`
}

// NutritionMeal is a saved meal on a captured local date.
type NutritionMeal struct {
	ID              uuid.UUID           `json:"id"`
	ClientRequestID uuid.UUID           `json:"client_request_id"`
	ConsumedAt      time.Time           `json:"consumed_at"`
	Timezone        string              `json:"timezone"`
	LocalDate       string              `json:"local_date"`
	MealType        string              `json:"meal_type"`
	InputMethod     string              `json:"input_method"`
	Name            string              `json:"name"`
	Notes           string              `json:"notes"`
	Revision        int                 `json:"revision"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	Items           []NutritionMealItem `json:"items"`
}

// NutritionMealPage contains meals and an optional next offset.
type NutritionMealPage struct {
	Meals      []NutritionMeal `json:"meals"`
	NextOffset *int            `json:"next_offset"`
}

// NutritionFavorite is one favourited food in the export. FoodName is empty when
// the food is no longer readable (for example a retired catalog entry).
type NutritionFavorite struct {
	FoodID      uuid.UUID `json:"food_id"`
	FoodName    string    `json:"food_name"`
	FavoritedAt time.Time `json:"favorited_at"`
}

// NutritionMealExportPage returns stored diary snapshots and a stable page cursor.
// Favorites is present (possibly empty) on the first page only and omitted on later pages.
type NutritionMealExportPage struct {
	Meals     []NutritionMeal     `json:"meals"`
	NextAfter *uuid.UUID          `json:"next_after"`
	Favorites []NutritionFavorite `json:"favorites,omitzero"` // nil (later pages) is omitted, [] is sent
}

// NutritionTotal reports a known subtotal and coverage among recorded items.
type NutritionTotal struct {
	KnownSubtotal float64 `json:"known_subtotal"`
	Unit          string  `json:"unit"`
	KnownItems    int     `json:"known_items"`
	Complete      bool    `json:"complete_for_logged_items"`
}

// NutritionDaily reports the logged foods and known nutrients on a local date.
type NutritionDaily struct {
	NutritionScores
	N2R       *N2RScore                 `json:"n2r,omitempty"`
	LocalDate string                    `json:"local_date"`
	Meals     int                       `json:"meal_count"`
	Items     int                       `json:"item_count"`
	Nutrients map[string]NutritionTotal `json:"nutrients"`
}

// NutritionGoalInput replaces the expected revision with user-chosen targets.
type NutritionGoalInput struct {
	Revision int                `json:"revision"`
	Targets  map[string]float64 `json:"targets"`
}

// NutritionGoals contains the latest nutrition target revision.
type NutritionGoals struct {
	NutritionGoalInput
	CreatedAt *time.Time `json:"created_at"`
}
