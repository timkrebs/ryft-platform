package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/nutrition"
	"github.com/timkrebs/ryft/internal/nutrition/importer"
	"github.com/timkrebs/ryft/internal/store"
)

const europeanMarkets = "AT,BE,CH,CZ,DE,DK,ES,FI,FR,GB,IE,IT,LU,NL,NO,PL,PT,SE"

type settings struct {
	provider, input, download, checksum, resume string
	write, stats                                bool
	maxMiB                                      int
	options                                     importer.Options
}

func readSettings(args []string) (settings, error) {
	var s settings
	f := flag.NewFlagSet("nutrition-import", flag.ContinueOnError)
	f.StringVar(&s.provider, "provider", "", "usda or off")
	f.StringVar(&s.input, "input", "", "local USDA CSV ZIP or OFF JSONL(.gz)")
	f.StringVar(&s.download, "url", "", "explicit official HTTPS release URL, staged to a temporary file")
	f.StringVar(&s.checksum, "sha256", "", "expected artifact SHA-256 hex (required with -url)")
	f.StringVar(&s.resume, "resume", "", "job UUID to resume, with the same release and options")
	f.StringVar(&s.options.Version, "version", "", "immutable release label")
	f.IntVar(&s.options.Limit, "limit", 200, "maximum input food records to scan")
	f.IntVar(&s.options.ChunkSize, "batch", 100, "batch size (1–2000)")
	f.IntVar(&s.options.MinCompleteness, "min-completeness", 0, "minimum percentage of core nutrients")
	f.Float64Var(&s.options.EnergyTolerance, "energy-tolerance", .35, "relative energy discrepancy warning threshold")
	f.IntVar(&s.maxMiB, "max-download-mib", 64, "maximum staged download size")
	f.BoolVar(&s.write, "write", false, "write to NUTRITION_IMPORT_DATABASE_URL (dry run by default)")
	f.BoolVar(&s.stats, "stats", false, "show nutrition catalog source counts")
	countries := f.String("markets", europeanMarkets, "comma-separated ISO country codes for OFF")
	if err := f.Parse(args); err != nil {
		return s, err
	}
	if f.NArg() > 0 {
		return s, errors.New("unexpected positional arguments")
	}
	if s.stats {
		if s.write || s.input != "" || s.download != "" || s.resume != "" {
			return s, errors.New("-stats is a read-only command")
		}
		return s, nil
	}
	if s.provider != "usda" && s.provider != "off" || (s.input == "") == (s.download == "") ||
		(s.resume != "" && !s.write) || !importer.ValidOptions(s.options) ||
		s.maxMiB < 1 || s.maxMiB > 4096 {
		return s, errors.New("specify a provider, release, and exactly one of -input or -url")
	}
	if s.checksum != "" || s.download != "" {
		if digest, err := hex.DecodeString(s.checksum); err != nil || len(digest) != sha256.Size {
			return s, errors.New("a valid -sha256 is required for downloads or checksum verification")
		}
	}
	if s.provider == "off" {
		s.options.Markets = strings.Split(strings.ToUpper(*countries), ",")
		for i, m := range s.options.Markets {
			m = strings.TrimSpace(m)
			if !importer.ValidOFFMarket(m) {
				return s, errors.New("market is outside the supported European selection")
			}
			s.options.Markets[i] = m
		}
		slices.Sort(s.options.Markets)
		s.options.Markets = slices.Compact(s.options.Markets)
	}
	return s, nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, log); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Error("nutrition import failed", "reason", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer, log *slog.Logger) error {
	s, err := readSettings(args)
	if err != nil {
		return err
	}
	var st *store.Store
	if s.write || s.stats {
		dsn := os.Getenv("NUTRITION_IMPORT_DATABASE_URL")
		if dsn == "" {
			return errors.New("NUTRITION_IMPORT_DATABASE_URL is required")
		}
		pool, err := db.Connect(ctx, dsn, 1)
		if err != nil {
			return errors.New("could not open import database")
		}
		defer pool.Close()
		st = store.New(pool)
	}
	if s.stats {
		counts, err := st.NutritionCatalogStats(ctx)
		if err != nil {
			return errors.New("could not read catalog statistics")
		}
		return json.NewEncoder(out).Encode(counts)
	}
	f, cleanup, err := stage(ctx, s)
	if err != nil {
		return err
	}
	defer cleanup()
	digest, err := artifactDigest(ctx, f, s.checksum)
	if err != nil {
		return err
	}
	runner := runImport{settings: s, store: st, log: log}
	if err := runner.start(ctx, digest); err != nil {
		return err
	}
	start := time.Now()
	err = runner.parse(ctx, f)
	if err == nil {
		err = runner.flush(ctx)
	}
	if s.write && !errors.Is(err, store.ErrNutritionConflict) {
		// A cancellation can leave a resumable running job. Attempt to record the
		// failure on a short independent context when the process can still connect.
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		reason := ""
		if err != nil {
			reason = "batch_or_parse_failed"
		}
		finishErr := st.FinishNutritionImport(finishCtx, runner.job, reason)
		cancel()
		if finishErr != nil && err == nil {
			err = errors.New("could not finalize import checkpoint")
		}
	}
	runner.job.Status = "complete"
	if err != nil {
		runner.job.Status = "failed"
	}
	log.Info("nutrition import finished", "provider", s.provider, "version", s.options.Version, "status", runner.job.Status,
		"write", s.write, "duration_ms", time.Since(start).Milliseconds(), "processed", runner.job.Counts.Processed)
	report := struct {
		nutrition.ImportJob
		Mode   string `json:"mode"`
		SHA256 string `json:"sha256"`
	}{ImportJob: runner.job, Mode: "dry_run", SHA256: hex.EncodeToString(digest[:])}
	if s.write {
		report.Mode = "write"
	}
	if reportErr := json.NewEncoder(out).Encode(report); err == nil {
		err = reportErr
	}
	if errors.Is(err, store.ErrNutritionConflict) {
		return errors.New("import checkpoint changed concurrently")
	}
	return err
}

type runImport struct {
	settings
	store   *store.Store
	log     *slog.Logger
	job     nutrition.ImportJob
	rows    []nutrition.CatalogRecord
	pending nutrition.ImportStats
	offset  int64
}

func (r *runImport) start(ctx context.Context, digest [32]byte) error {
	r.job = nutrition.ImportJob{Provider: r.provider, Version: r.options.Version, DatasetHash: digest,
		Counts: nutrition.ImportStats{Reasons: map[string]int{}}}
	if r.store == nil {
		return nil
	}
	if r.resume != "" {
		id, err := uuid.Parse(r.resume)
		if err != nil || id == uuid.Nil {
			return errors.New("invalid resume job ID")
		}
		r.job.ID = id
	}
	config, err := json.Marshal(struct {
		Mapping string
		Options importer.Options
	}{importer.MappingVersion, r.options})
	if err != nil {
		return err
	}
	r.job.OptionsHash = sha256.Sum256(config)
	r.job, err = r.store.StartNutritionImport(ctx, r.job)
	if err != nil {
		return errors.New("could not start import or resume with the specified release, checksum and options")
	}
	r.options.After = r.job.Checkpoint
	r.log.Info("nutrition import started", "provider", r.provider, "job", r.job.ID, "checkpoint", r.job.Checkpoint)
	return nil
}

func (r *runImport) parse(ctx context.Context, f *os.File) error {
	emit := func(res importer.Result) error {
		r.offset = res.Offset
		r.pending.Processed++
		switch res.Outcome {
		case "valid":
			r.pending.Valid++
		case "valid_with_warnings":
			r.pending.Warnings++
		case "filtered":
			r.pending.Filtered++
		case "quarantined":
			r.pending.Quarantined++
		case "rejected":
			r.pending.Rejected++
		}
		if r.pending.Reasons == nil {
			r.pending.Reasons = map[string]int{}
		}
		for _, code := range res.Reasons {
			r.pending.Reasons[code]++
		}
		if res.Record != nil && r.store != nil {
			r.rows = append(r.rows, *res.Record)
		}
		if r.pending.Processed == r.options.ChunkSize {
			return r.flush(ctx)
		}
		return nil
	}
	if r.provider == "usda" {
		return importer.USDA(ctx, f.Name(), r.options, emit)
	}
	return importer.OFF(ctx, f, r.options, emit)
}

func (r *runImport) flush(ctx context.Context) error {
	if r.pending.Processed == 0 {
		return nil
	}
	if r.store == nil {
		r.job.Checkpoint = r.offset
		r.job.Counts.Add(r.pending)
	} else {
		for attempt := 0; ; attempt++ {
			job, err := r.store.ApplyNutritionImport(ctx, r.job, r.offset, r.rows, r.pending)
			if err == nil {
				r.job = job
				break
			}
			if !store.RetryNutritionImport(err) || attempt >= 2 {
				if errors.Is(err, store.ErrNutritionConflict) {
					return err
				}
				return errors.New("database did not commit the import batch")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
			}
		}
	}
	r.pending, r.rows = nutrition.ImportStats{}, r.rows[:0]
	return nil
}

func artifactDigest(ctx context.Context, file *os.File, expected string) ([32]byte, error) {
	h := sha256.New()
	var buf [64 << 10]byte
	for {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, err
		}
		n, err := file.Read(buf[:])
		_, _ = h.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return [32]byte{}, errors.New("could not hash staged artifact")
		}
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	if expected != "" && !strings.EqualFold(expected, hex.EncodeToString(digest[:])) {
		return digest, errors.New("staged artifact SHA-256 mismatch")
	}
	_, err := file.Seek(0, io.SeekStart)
	return digest, err
}

func stage(ctx context.Context, s settings) (*os.File, func(), error) {
	if s.input != "" {
		file, err := os.Open(s.input)
		if err != nil {
			return nil, nil, errors.New("could not open staged input")
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return nil, nil, errors.New("input must be a regular staged file")
		}
		return file, func() { _ = file.Close() }, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.download, nil)
	if err != nil || !releaseURL(req.URL, s.provider) {
		return nil, nil, errors.New("download URL must use the provider's official HTTPS host")
	}
	req.Header.Set("User-Agent", "RYFT Nutrition Dataset Import (https://ryftlab.de)")
	client := &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !releaseURL(req.URL, s.provider) {
			return errors.New("unexpected dataset redirect")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, errors.New("dataset download failed")
	}
	defer resp.Body.Close()
	limit := int64(s.maxMiB) << 20
	if resp.StatusCode != http.StatusOK || resp.ContentLength > limit {
		return nil, nil, fmt.Errorf("dataset response refused (HTTP %d or size limit)", resp.StatusCode)
	}
	file, err := os.CreateTemp("", "ryft-nutrition-dataset-*")
	if err != nil {
		return nil, nil, errors.New("could not stage dataset")
	}
	cleanup := func() { _ = file.Close(); _ = os.Remove(file.Name()) }
	n, err := io.Copy(file, io.LimitReader(resp.Body, limit+1))
	if err != nil || n > limit {
		cleanup()
		return nil, nil, errors.New("download was interrupted or exceeded the staging size limit")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, errors.New("could not rewind staged dataset")
	}
	return file, cleanup, nil
}

func releaseURL(u *url.URL, provider string) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	switch provider {
	case "usda":
		return (u.Hostname() == "fdc.nal.usda.gov" || u.Hostname() == "www.ars.usda.gov") && strings.HasSuffix(u.Path, ".zip")
	case "off":
		return u.Hostname() == "static.openfoodfacts.org" && strings.HasSuffix(u.Path, ".jsonl.gz")
	}
	return false
}
