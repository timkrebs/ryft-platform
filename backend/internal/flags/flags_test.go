package flags

import (
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

var (
	alice = uuid.MustParse("11111111-2222-3333-4444-555555555555")
	bob   = uuid.MustParse("00000000-0000-0000-0000-000000000001")
)

// The bucket is part of the contract with operations (a rollout must not
// reshuffle who has a flag), so its values are pinned; docs/API.md → Remote
// config gives the same formula (checked there with Python's hashlib).
func TestBucketIsPinned(t *testing.T) {
	for _, c := range []struct {
		user uuid.UUID
		key  string
		want int
	}{
		{alice, "today.redesign", 88}, {alice, "push.enabled", 66}, {alice, "train.enabled", 70},
		{bob, "today.redesign", 87}, {bob, "push.enabled", 14}, {bob, "train.enabled", 93},
	} {
		if got := Bucket(c.user, c.key); got != c.want {
			t.Errorf("Bucket(%s, %s) = %d, want %d", c.user, c.key, got, c.want)
		}
	}
}

func TestBucketSpreadsUsers(t *testing.T) {
	var counts [100]int
	for k := 0; k < 20000; k++ {
		counts[Bucket(uuid.New(), "push.enabled")]++
	}
	for b, n := range counts {
		if n < 120 || n > 290 { // 200 expected per bucket
			t.Fatalf("bucket %d has %d of 20000 users", b, n)
		}
	}
	if Bucket(alice, "push.enabled") == Bucket(alice, "today.redesign") && Bucket(bob, "push.enabled") == Bucket(bob, "today.redesign") {
		t.Fatal("buckets must differ between flags")
	}
}

func TestOn(t *testing.T) {
	f := model.FeatureFlag{Key: "today.redesign", Enabled: true}
	if On(f, alice) {
		t.Fatal("0 %: off")
	}
	f.AllowUserIDs = []uuid.UUID{alice}
	if !On(f, alice) || On(f, bob) {
		t.Fatal("the allow-list turns it on for alice only")
	}
	f.RolloutPct = 100
	if !On(f, bob) {
		t.Fatal("100 %: on for everyone")
	}
	f.RolloutPct = Bucket(bob, f.Key) // bob's bucket is not below it
	if On(f, bob) {
		t.Fatal("rollout_pct is exclusive of the user's own bucket")
	}
	f.RolloutPct++
	if !On(f, bob) {
		t.Fatal("one more percent includes bob")
	}
	f.Enabled = false
	if On(f, alice) || On(f, bob) {
		t.Fatal("disabled: off for everyone, the allow-list included")
	}
}

func TestEvaluateAndKeys(t *testing.T) {
	got := Evaluate([]model.FeatureFlag{{Key: "train.enabled", Enabled: true, RolloutPct: 100},
		{Key: "coach.enabled", Enabled: false, RolloutPct: 100}}, alice)
	if len(got) != 2 || !got["train.enabled"] || got["coach.enabled"] {
		t.Fatalf("evaluate: %v", got)
	}
	for k, want := range map[string]bool{"train.enabled": true, "sleep.planner": true, "a.b_c.d2": true,
		"train": false, "Train.enabled": false, ".x": false, "x.": false, "x..y": false, "x.1y": false,
		"x-y.z": false, "a." + string(make([]byte, 70)): false} {
		if ValidKey(k) != want {
			t.Errorf("ValidKey(%q) = %v", k, !want)
		}
	}
}
