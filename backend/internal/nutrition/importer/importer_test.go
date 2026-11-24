package importer

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func usdaFixture(t *testing.T) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "fixture.zip")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	files, err := os.ReadDir("testdata/usda")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range files {
		data, err := os.ReadFile(filepath.Join("testdata", "usda", item.Name()))
		if err != nil {
			t.Fatal(err)
		}
		w, err := z.Create("Fixture/" + item.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func testOptions() Options {
	return Options{Version: "fixture", Limit: 10, ChunkSize: 1, EnergyTolerance: .35, Markets: []string{"DE", "AT", "CH"}}
}

func TestUSDAMapping(t *testing.T) {
	var got []Result
	if err := USDA(context.Background(), usdaFixture(t), testOptions(), func(r Result) error {
		got = append(got, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1].Outcome != "valid_with_warnings" || got[2].Outcome != "filtered" {
		t.Fatalf("report: %+v", got)
	}
	food := got[0].Record
	for _, n := range food.Nutrients {
		switch n.Code {
		case "energy_kcal":
			if n.Amount != 42 {
				t.Fatalf("wrong Atwater preference: %+v", n)
			}
		case "fat":
			if n.Amount != 0 {
				t.Fatalf("explicit zero lost: %+v", n)
			}
		case "folate":
			if n.Amount != 4.99 {
				t.Fatalf("not DFE: %+v", n)
			}
		case "carbohydrates", "sodium":
			t.Fatalf("unknown value invented: %+v", n)
		}
	}
	if len(food.Nutrients) != 6 || len(food.Portions) != 1 || *food.Portions[0].WeightG != 150 ||
		got[1].Record.Portions[0].Label != "2 cup level" {
		t.Fatalf("bad parsed foods: %+v %+v", food, got[1].Record)
	}
}

func TestOFFMappingAndResume(t *testing.T) {
	data, err := os.ReadFile("testdata/off.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	var got []Result
	if err := OFF(context.Background(), strings.NewReader(string(data)), options, func(r Result) error {
		got = append(got, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[2].Outcome != "valid_with_warnings" || got[3].Outcome != "quarantined" || got[4].Outcome != "filtered" {
		t.Fatalf("report: %+v", got)
	}
	food := got[0].Record
	if len(food.Names) != 2 || food.Barcodes[0].GTIN != "00012345678905" ||
		len(food.Nutrients) != 7 || len(got[2].Record.Nutrients) != 0 || !slices.Contains(got[2].Reasons, "ambiguous_100g_ml") {
		t.Fatalf("OFF mapping: %+v %+v", food, got[2])
	}
	for _, n := range food.Nutrients {
		if n.Code == "sodium" && (n.Amount != 120 || n.Unit != "mg") {
			t.Fatalf("OFF sodium: %+v", n)
		}
	}
	if n := got[1].Record.Nutrients; len(n) != 2 || n[0].ReferenceUnit != "serving" || got[1].Record.Portions[0].WeightG != nil {
		t.Fatalf("mixed bases: %+v", got[1].Record)
	}
	stop := errors.New("batch interrupted")
	options.After, options.Limit = 2, 4
	if err := OFF(context.Background(), strings.NewReader(string(data)), options, func(r Result) error {
		if r.Offset != 3 {
			t.Fatalf("resume offset: %d", r.Offset)
		}
		return stop
	}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if err := OFF(context.Background(), io.LimitReader(strings.NewReader(string(data)), 4), options, func(Result) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
