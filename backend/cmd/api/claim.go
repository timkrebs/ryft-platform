package main

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/metrics"
)

// claimTimeout bounds one claim: it must not hold up a request that does not depend on it.
const claimTimeout = 3 * time.Second

// deviceClaimer is what claimDevice needs of the store (*store.Store; a counting fake in tests).
type deviceClaimer interface {
	ClaimDevice(ctx context.Context, userID, deviceID uuid.UUID) (int, error)
}

// claimDevice makes the request's install (X-RYFT-Device-ID) the caller's: an install's push
// tokens follow the account that used it last (D251). It runs after the bearer, the user load
// and App Attest and before the beta gate, so a request the invite gate refuses still claims.
//
// No header, one that is not a UUID or is the nil UUID, or a route App Attest exempts
// (attestExempt): nothing happens. The device is in the caller's
// push_devices (the user row is loaded anyway): nothing happens, no query. Otherwise one
// ClaimDevice. It is best effort: a failed claim is logged (no ids, no token) and the request is
// served; the next request from the install tries again, as nothing was recorded.
func claimDevice(st deviceClaimer, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The routes App Attest exempts never claim: under APP_ATTEST=on only a request with an
			// app token may end another account's alerts. The nil UUID is no install's id.
			if device, ok := deviceOf(r); ok && device != uuid.Nil && !attestExempt(r) {
				u := userOf(r)
				if !slices.Contains(u.PushDevices, device) {
					ctx, cancel := context.WithTimeout(r.Context(), claimTimeout)
					_, err := st.ClaimDevice(ctx, u.ID, device)
					cancel()
					if err != nil {
						metrics.PushDeviceClaims.WithLabelValues("failed").Inc()
						log.WarnContext(r.Context(), "claim device failed", "err", err)
					} else {
						metrics.PushDeviceClaims.WithLabelValues("claimed").Inc()
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
