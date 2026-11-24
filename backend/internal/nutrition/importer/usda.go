package importer

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
)

// Where the release reports multiple energy methods, prefer Atwater specific,
// then legacy Energy, then Atwater general. Other mappings preserve chemistry.
var usdaNutrients = map[string]nutrientKey{
	"1008": {"energy_kcal", "kcal"}, "2047": {"energy_kcal", "kcal"}, "2048": {"energy_kcal", "kcal"},
	"1062": {"energy_kj", "kJ"}, "1003": {"protein", "g"}, "1004": {"fat", "g"},
	"1005": {"carbohydrates_total", "g"}, "1079": {"fiber", "g"}, "2000": {"total_sugar", "g"},
	"1235": {"added_sugar", "g"}, "1258": {"saturated_fat", "g"}, "1292": {"monounsaturated_fat", "g"},
	"1293": {"polyunsaturated_fat", "g"}, "1257": {"trans_fat", "g"}, "1278": {"epa", "g"}, "1272": {"dha", "g"},
	"1093": {"sodium", "mg"}, "1092": {"potassium", "mg"}, "1087": {"calcium", "mg"},
	"1090": {"magnesium", "mg"}, "1089": {"iron", "mg"}, "1095": {"zinc", "mg"}, "1091": {"phosphorus", "mg"},
	"1103": {"selenium", "ug"}, "1106": {"vitamin_a", "ug"}, "1162": {"vitamin_c", "mg"},
	"1114": {"vitamin_d", "ug"}, "1109": {"vitamin_e", "mg"}, "1185": {"vitamin_k", "ug"},
	"1165": {"vitamin_b1", "mg"}, "1166": {"vitamin_b2", "mg"}, "1167": {"vitamin_b3", "mg"},
	"1175": {"vitamin_b6", "mg"}, "1178": {"vitamin_b12", "ug"}, "1190": {"folate", "ug"},
	"1057": {"caffeine", "mg"}, "1018": {"alcohol", "g"}, "1051": {"water", "g"}, "1253": {"cholesterol", "mg"},
}

const csvMaxRecord = 2 << 20

// csvLimit caps each logical record, including quoted fields that span lines.
// encoding/csv may have its own small read-ahead buffer.
type csvLimit struct {
	io.Reader
	remaining int
}

func (r *csvLimit) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("USDA CSV record exceeds 2 MiB")
	}
	n, err := r.Reader.Read(p[:min(r.remaining, len(p))])
	r.remaining -= n
	return n, err
}

type csvTable struct {
	reader *csv.Reader
	limit  *csvLimit
	cols   map[string]int
}

func (t csvTable) read() ([]string, error) {
	t.limit.remaining = csvMaxRecord
	return t.reader.Read()
}

func (t csvTable) at(row []string, name string) string {
	if i, ok := t.cols[name]; ok {
		return row[i]
	}
	return ""
}

type usdaArchive map[string]*zip.File

func (a usdaArchive) open(name string, required ...string) (csvTable, io.Closer, error) {
	f := a[name]
	if f == nil {
		return csvTable{}, nil, fmt.Errorf("USDA archive is missing %s", name)
	}
	r, err := f.Open()
	if err != nil {
		return csvTable{}, nil, err
	}
	limit := &csvLimit{Reader: r, remaining: csvMaxRecord}
	csvReader := csv.NewReader(limit)
	csvReader.ReuseRecord = true
	header, err := csvReader.Read()
	if err != nil {
		_ = r.Close()
		return csvTable{}, nil, fmt.Errorf("invalid USDA header: %s", name)
	}
	table := csvTable{reader: csvReader, limit: limit, cols: make(map[string]int)}
	for i, col := range header {
		table.cols[strings.TrimPrefix(col, "\ufeff")] = i
	}
	for _, col := range required {
		if _, ok := table.cols[col]; !ok {
			_ = r.Close()
			return csvTable{}, nil, fmt.Errorf("USDA table %s is missing column %s", name, col)
		}
	}
	return table, r, nil
}

func (a usdaArchive) scan(ctx context.Context, name string, required []string, fn func(csvTable, []string) error) error {
	t, closer, err := a.open(name, required...)
	if err != nil {
		return err
	}
	defer closer.Close()
	for rowNum := 1; ; rowNum++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := t.read()
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return fmt.Errorf("invalid USDA CSV in %s at data row %d", name, rowNum)
		}
		if err := fn(t, row); err != nil {
			return err
		}
	}
}

type usdaFood struct {
	id       string
	offset   int64
	r        nutrition.CatalogRecord
	values   map[string]model.FoodNutrient
	valueIDs map[string]string
	warnings []string
	rejected string
}

// USDA reads Foundation, SR Legacy and Survey CSV zip releases. Only one food
// chunk and its child rows are retained at a time. The join does not assume CSV sort order.
func USDA(ctx context.Context, filename string, o Options, emit Emit) error {
	if !ValidOptions(o) {
		return nutrition.ErrInvalid
	}
	z, err := zip.OpenReader(filename)
	if err != nil {
		return err
	}
	defer z.Close()
	a := usdaArchive{}
	var bytes uint64
	for _, f := range z.File {
		name := path.Base(f.Name)
		if !slices.Contains([]string{"food.csv", "nutrient.csv", "food_nutrient.csv", "food_portion.csv", "measure_unit.csv"}, name) {
			continue
		}
		if a[name] != nil || f.UncompressedSize64 > 3<<30 || bytes+f.UncompressedSize64 > 5<<30 {
			return errors.New("USDA archive has ambiguous or oversized tables")
		}
		bytes += f.UncompressedSize64
		a[name] = f
	}
	units := map[string]string{}
	if err := a.scan(ctx, "nutrient.csv", []string{"id", "unit_name"}, func(t csvTable, row []string) error {
		id := t.at(row, "id")
		if _, ok := usdaNutrients[id]; ok {
			units[id] = t.at(row, "unit_name")
		}
		return nil
	}); err != nil {
		return err
	}
	measures := map[string]string{}
	if a["measure_unit.csv"] != nil {
		if err := a.scan(ctx, "measure_unit.csv", []string{"id", "name"}, func(t csvTable, row []string) error {
			if len(measures) >= 10000 {
				return errors.New("too many USDA measure units")
			}
			label := t.at(row, "name")
			if nutrition.Text(label, 200) {
				measures[t.at(row, "id")] = label
			}
			return nil
		}); err != nil {
			return err
		}
	}
	foods, closer, err := a.open("food.csv", "fdc_id", "data_type", "description")
	if err != nil {
		return err
	}
	defer closer.Close()
	for offset := int64(0); offset < int64(o.Limit); {
		chunk := make([]usdaFood, 0, o.ChunkSize)
		for len(chunk) < o.ChunkSize && offset < int64(o.Limit) {
			if err := ctx.Err(); err != nil {
				return err
			}
			row, err := foods.read()
			if errors.Is(err, io.EOF) {
				break
			}
			offset++
			if err != nil {
				return fmt.Errorf("invalid USDA food CSV at data row %d", offset)
			}
			if offset <= o.After {
				continue
			}
			chunk = append(chunk, usdaIdentity(foods, row, offset, o.Version))
		}
		if len(chunk) == 0 {
			break
		}
		if err := a.usdaChildren(ctx, chunk, units, measures); err != nil {
			return err
		}
		for _, f := range chunk {
			if f.rejected != "" {
				outcome := "rejected"
				if f.rejected == "data_type" {
					outcome = "filtered"
				}
				if err := emit(Result{Offset: f.offset, Outcome: outcome, Reasons: []string{f.rejected}}); err != nil {
					return err
				}
				continue
			}
			keys := make([]string, 0, len(f.values))
			for code := range f.values {
				keys = append(keys, code)
			}
			slices.Sort(keys)
			for _, code := range keys {
				f.r.Nutrients = append(f.r.Nutrients, f.values[code])
			}
			if err := emit(result(f.offset, f.r, f.warnings, o)); err != nil {
				return err
			}
		}
	}
	return nil
}

func usdaIdentity(t csvTable, row []string, offset int64, version string) usdaFood {
	id, typ := t.at(row, "fdc_id"), t.at(row, "data_type")
	f := usdaFood{id: id, offset: offset, values: map[string]model.FoodNutrient{}, valueIDs: map[string]string{}}
	if !slices.Contains([]string{"foundation_food", "sr_legacy_food", "survey_fndds_food"}, typ) {
		f.rejected = "data_type"
		return f
	}
	if n, err := strconv.ParseUint(id, 10, 64); err != nil || n == 0 || strconv.FormatUint(n, 10) != id {
		f.rejected = "invalid_identity"
		return f
	}
	name := strings.TrimSpace(t.at(row, "description"))
	if !utf8.ValidString(name) {
		// FoodData Central documents older CSV releases as Latin-1.
		var b strings.Builder
		for _, c := range []byte(name) {
			b.WriteRune(rune(c))
		}
		name = b.String()
	}
	f.r = nutrition.CatalogRecord{
		Name: name, Type: "generic", Quality: "source_reported", Provider: "usda", ExternalID: id,
		Version: version, License: "CC0-1.0", URL: "https://fdc.nal.usda.gov/food-details/" + id + "/nutrients",
		Names: []model.FoodName{{Language: "en", Label: name}},
	}
	if date, err := time.Parse(time.DateOnly, t.at(row, "publication_date")); err == nil {
		f.r.SourceUpdatedAt = &date
	}
	return f
}

func (a usdaArchive) usdaChildren(ctx context.Context, chunk []usdaFood, units, measures map[string]string) error {
	idx := make(map[string]*usdaFood, len(chunk))
	for i := range chunk {
		f := &chunk[i]
		if f.rejected != "" {
			continue
		}
		if idx[f.id] != nil {
			f.rejected = "duplicate_food_id"
			continue
		}
		idx[f.id] = f
	}
	if len(idx) == 0 {
		return nil
	}
	if err := a.scan(ctx, "food_nutrient.csv", []string{"fdc_id", "nutrient_id", "amount"}, func(t csvTable, row []string) error {
		f := idx[t.at(row, "fdc_id")]
		id := t.at(row, "nutrient_id")
		n, known := usdaNutrients[id]
		if f == nil || !known || t.at(row, "amount") == "" {
			return nil
		}
		amount, ok := parseAmount(t.at(row, "amount"))
		value, err := mapped(n, units[id], amount, 100, "g", "fdc:"+id)
		if !ok || err != nil {
			warn(f, "invalid_nutrient")
		} else {
			f.addNutrient(n.code, id, value)
		}
		return nil
	}); err != nil {
		return err
	}
	if a["food_portion.csv"] == nil {
		return nil
	}
	return a.scan(ctx, "food_portion.csv", []string{"fdc_id", "gram_weight"}, func(t csvTable, row []string) error {
		f := idx[t.at(row, "fdc_id")]
		if f == nil {
			return nil
		}
		label := t.at(row, "portion_description")
		if label == "" {
			parts := []string{t.at(row, "amount"), measures[t.at(row, "measure_unit_id")], t.at(row, "modifier")}
			label = strings.Join(slices.DeleteFunc(parts, func(s string) bool { return strings.TrimSpace(s) == "" }), " ")
		}
		weight, valid := parseAmount(t.at(row, "gram_weight"))
		// Each source row's label corresponds to the whole recorded gram weight.
		p := model.FoodPortionInput{Label: label, Quantity: 1, Unit: "portion", WeightG: &weight}
		if valid && nutrition.ValidPortion(p) && len(f.r.Portions) < 50 {
			f.r.Portions = append(f.r.Portions, p)
		} else {
			warn(f, "invalid_or_excess_portion")
		}
		return nil
	})
}

func warn(f *usdaFood, code string) {
	if !slices.Contains(f.warnings, code) {
		f.warnings = append(f.warnings, code)
	}
}

func (f *usdaFood) addNutrient(code, id string, value model.FoodNutrient) {
	old := f.valueIDs[code]
	switch {
	case old == id:
		if f.values[code].Amount != value.Amount {
			warn(f, "duplicate_nutrient")
		}
	case old == "" || id == "2048" || (id == "1008" && old == "2047"):
		f.values[code], f.valueIDs[code] = value, id
	}
}
