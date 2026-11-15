package httpx

import (
	"context"
	"time"
)

type stopKey struct{}

// WithStop marks ctx, a server's base context (http.Server.BaseContext), as its hard stop:
// internal/app cancels it when the shutdown's HTTP step runs out of time, which ends every
// request's context, and Detach's too.
func WithStop(ctx context.Context) context.Context {
	return context.WithValue(ctx, stopKey{}, ctx)
}

// Detach is ctx without the client's cancel, for work that must finish even when the
// client hangs up (an account deletion, an audit row): it keeps ctx's values (the request's
// log fields) and ends after d, or when the server stops hard (WithStop). So such work
// cannot hold a database connection past the shutdown's HTTP step (review-a1 finding 6),
// where the pool closes next.
func Detach(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	out, cancel := context.WithTimeout(context.WithoutCancel(ctx), d)
	stop, ok := ctx.Value(stopKey{}).(context.Context)
	if !ok {
		return out, cancel
	}
	unhook := context.AfterFunc(stop, cancel)
	return out, func() { unhook(); cancel() }
}
