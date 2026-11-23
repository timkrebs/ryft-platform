package connect_test

import (
	"context"
	"slices"
	"testing"

	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/connect/fake"
)

func TestRegistry(t *testing.T) {
	r, err := connect.NewRegistry(fake.New("s"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("fake"); !ok {
		t.Fatal("fake is not served")
	}
	if _, ok := r.Get("garmin"); ok {
		t.Fatal("garmin is served without being registered")
	}
	if got := r.Names(); !slices.Equal(got, []string{"fake"}) {
		t.Fatalf("Names = %v", got)
	}
	if got, other := r.DisplayName("fake"), r.DisplayName("garmin"); got != "Fake" || other != "Garmin" {
		t.Fatalf("DisplayName = %q, %q", got, other)
	}
	if _, err := connect.NewRegistry(fake.New("s"), fake.New("t")); err == nil {
		t.Fatal("a provider twice was accepted")
	}
	// ingest's view holds the push half only: the type has no OAuth method.
	push := r.PushSide()
	ps, ok := push.Get("fake")
	if !ok {
		t.Fatal("the push registry lacks fake")
	}
	// P2-7: the push registry's values expose the push methods only; a type assertion reaches no token method.
	if _, isOAuth := ps.(connect.OAuth); isOAuth {
		t.Fatal("the push side is also an OAuth: a type assertion reaches the tokens' half")
	}
	if _, isProvider := ps.(connect.Provider); isProvider {
		t.Fatal("the push side is also a Provider")
	}
	var asAny any = ps
	if _, ok := asAny.(interface {
		Refresh(context.Context, string) (connect.Grant, error)
	}); ok {
		t.Fatal("the push side has Refresh")
	}
	if _, ok := asAny.(interface {
		Deregister(context.Context, connect.Grant) error
	}); ok {
		t.Fatal("the push side has Deregister")
	}
	if _, ok := asAny.(interface{ Name() string }); ok {
		t.Fatal("the push side has the provider's identity methods")
	}
	if got := ps.MaxPushBytes(); got != connect.MaxPushBytes {
		t.Fatalf("the wrapper does not forward: MaxPushBytes %d", got)
	}
	if got := ps.Required(); !slices.Equal(got, []string{"read_hrv", "read_sleep"}) {
		t.Fatalf("the wrapper does not forward: Required %v", got)
	}
	if _, err := connect.NewRegistry(renamed{fake.New("s"), "unknown"}); err == nil {
		t.Fatal(`the reserved id "unknown" was accepted as a provider`)
	}
	var nilReg *connect.Registry
	if _, ok := nilReg.Get("fake"); ok || nilReg.Names() != nil {
		t.Fatal("a nil registry serves something")
	}
}

// renamed is the fake under another name.
type renamed struct {
	*fake.Provider
	name string
}

func (r renamed) Name() string { return r.name }
