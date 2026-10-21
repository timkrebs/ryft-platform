package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/smoke"
)

// smokeMain runs internal/smoke from the environment:
//
//	SMOKE_API_URL, SMOKE_INGEST_URL  the services (in the cluster: their Service DNS names)
//	SMOKE_HEALTH_URLS                comma-separated readiness URLs, each must answer 200
//	SMOKE_TOKEN                      a bearer instead of the sign-in (the local stack)
//	SUPABASE_URL or SUPABASE_PROJECT_REF, SUPABASE_ANON_KEY, SMOKE_EMAIL, SMOKE_PASSWORD
//	SMOKE_WAIT                       how long scoring may take (default 60s)
func smokeMain() int {
	log := httpx.Logger("smoke")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	c := smoke.Config{
		APIURL:      strings.TrimRight(os.Getenv("SMOKE_API_URL"), "/"),
		IngestURL:   strings.TrimRight(os.Getenv("SMOKE_INGEST_URL"), "/"),
		Token:       os.Getenv("SMOKE_TOKEN"),
		SupabaseURL: os.Getenv("SUPABASE_URL"),
		AnonKey:     os.Getenv("SUPABASE_ANON_KEY"),
		Email:       os.Getenv("SMOKE_EMAIL"),
		Password:    os.Getenv("SMOKE_PASSWORD"),
		Log:         log,
	}
	if c.SupabaseURL == "" && os.Getenv("SUPABASE_PROJECT_REF") != "" {
		c.SupabaseURL = "https://" + os.Getenv("SUPABASE_PROJECT_REF") + ".supabase.co"
	}
	for _, u := range strings.Split(os.Getenv("SMOKE_HEALTH_URLS"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			c.Health = append(c.Health, u)
		}
	}
	if v := os.Getenv("SMOKE_WAIT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Error("SMOKE_WAIT", "err", err)
			return 2
		}
		c.Wait = d
	}
	if c.APIURL == "" || c.IngestURL == "" {
		log.Error("set SMOKE_API_URL and SMOKE_INGEST_URL")
		return 2
	}
	res, err := smoke.Run(ctx, c)
	if err != nil {
		log.Error("smoke failed", "err", err, "user_id", res.UserID, "day", res.Day)
		return 1
	}
	b, _ := json.Marshal(res)
	log.Info("smoke passed", "result", json.RawMessage(b))
	return 0
}
