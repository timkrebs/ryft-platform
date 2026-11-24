package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// NutritionSearchResults records the size of each successful search response.
	NutritionSearchResults = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ryft_nutrition_search_results",
		Help:    "Number of foods returned by successful Nutrition searches.",
		Buckets: []float64{0, 1, 5, 10, 20, 30, 50},
	})
	// NutritionBarcode counts valid barcode requests by match outcome.
	NutritionBarcode = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_nutrition_barcode_lookups_total",
		Help: "Successful local barcode lookups by match outcome.",
	}, []string{"result"})
)

// NutritionDBDuration records the database path for frequently used Nutrition operations.
var NutritionDBDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "ryft_nutrition_db_duration_seconds",
	Help:    "Time spent in the database path by Nutrition operation.",
	Buckets: []float64{.001, .005, .01, .025, .05, .1, .2, .5, 1, 2, 5, 15},
}, []string{"operation"})
