package pubsub

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
)

// repo is the repository root, seen from this package's directory.
const repo = "../../../.."

// The committed tfvars is B's contract with infra/gcp: what the generator writes must be
// it, byte for byte (cmd/pubsub-topology -check runs the same comparison in CI).
func TestTFVarsGolden(t *testing.T) {
	want, err := os.ReadFile(filepath.Join(repo, TFVarsFile))
	if err != nil {
		t.Fatal(err)
	}
	got, err := TFVars(Spec())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("TFVars(Spec()) differs from %s:\n--- got\n%s", TFVarsFile, got)
	}
}

func TestEmulatorInitGolden(t *testing.T) {
	want, err := os.ReadFile(filepath.Join(repo, EmulatorInitFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := EmulatorInit(Spec()); !bytes.Equal(got, want) {
		t.Fatalf("EmulatorInit(Spec()) differs from %s: run `go run ./cmd/pubsub-topology` in backend/", EmulatorInitFile)
	}
}

// The script's shape: a header that says it is generated, the default project the
// services use with the emulator, and one line per topic and subscription.
func TestEmulatorInitContents(t *testing.T) {
	s := string(EmulatorInit(Spec()))
	for _, want := range []string{
		"#!/bin/sh\n", "DO NOT EDIT", "set -eu\n",
		`: "${GCP_PROJECT:=` + config.EmulatorProject + `}"`,
		`"maxDeliveryAttempts":20`, `"minimumBackoff":"10s","maximumBackoff":"600s"`, `"ackDeadlineSeconds":60,`,
		`"pushConfig":{"pushEndpoint":"http://%s:8080%s"}`,
		"push scoring-ingested ryft-ingested scoring /push/ingested true 300 604800 ryft-dlq-scoring\n",
		"push notify-wait ryft-notify-wait notify /push/wait false 60 604800 ryft-dlq-notify\n",
		"push tick-api ryft-tick api /internal/tick false 600 600\n",
		"hold dlq-scoring-hold ryft-dlq-scoring 1209600\n",
		"409) echo",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("init.sh lacks %q", want)
		}
	}
	top := Spec()
	if n := strings.Count(s, "\ntopic "); n != len(top.Topics) {
		t.Errorf("%d topic lines, want %d", n, len(top.Topics))
	}
	if n := strings.Count(s, "\npush "); n != len(top.Push) {
		t.Errorf("%d push lines, want %d", n, len(top.Push))
	}
	if n := strings.Count(s, "\nhold "); n != len(top.Hold) {
		t.Errorf("%d hold lines, want %d", n, len(top.Hold))
	}
	if strings.Contains(s, "%!") {
		t.Error("a format verb went wrong in the script")
	}
}

// Terraform's retry policy and attempt count are constants in pubsub.tf; the emulator's
// script is written from these, so they must be the same numbers.
func TestTerraformConstants(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repo, "infra/gcp/pubsub.tf"))
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		m := regexp.MustCompile(`(?m)^\s*` + name + `\s*=\s*"?([0-9]+s?)"?\s*$`).FindSubmatch(raw)
		if m == nil {
			t.Fatalf("pubsub.tf has no %s", name)
		}
		return string(m[1])
	}
	if got, want := read("retry_minimum_backoff"), strconv.Itoa(int(RetryMinimumBackoff/time.Second))+"s"; got != want {
		t.Errorf("retry_minimum_backoff %s, want %s", got, want)
	}
	if got, want := read("retry_maximum_backoff"), strconv.Itoa(int(RetryMaximumBackoff/time.Second))+"s"; got != want {
		t.Errorf("retry_maximum_backoff %s, want %s", got, want)
	}
	if got := read("max_delivery_attempts"); got != strconv.Itoa(MaxDeliveryAttempts) {
		t.Errorf("max_delivery_attempts %s, want %d", got, MaxDeliveryAttempts)
	}
	if !regexp.MustCompile(`ack_deadline_seconds\s*=\s*` + strconv.Itoa(int(HoldAckDeadline/time.Second)) + `\b`).Match(raw) {
		t.Errorf("pubsub.tf's hold subscriptions do not ack within %s", HoldAckDeadline)
	}
}

func TestSpecValidates(t *testing.T) {
	if err := Spec().Validate(); err != nil {
		t.Fatal(err)
	}
	broken := []func(*Topology){
		func(t *Topology) { t.Topics = append(t.Topics, t.Topics[0]) },
		func(t *Topology) { t.Push[0].Topic = "ryft-nowhere" },
		func(t *Topology) { t.Push[0].DeadLetterTopic = t.Push[0].Topic },
		func(t *Topology) { t.Push[0].DeadLetterTopic = "ryft-dlq-nowhere" },
		func(t *Topology) { t.Push[0].Ordered = !t.Push[0].Ordered },
		func(t *Topology) { t.Push[0].AckDeadline = 5 * time.Second },
		func(t *Topology) { t.Push[0].Retention = 8 * 24 * time.Hour },
		func(t *Topology) { t.Push = slices.Delete(t.Push, 0, 1) }, // scoring-ingested gone
		func(t *Topology) { t.Hold[0].Topic = "ryft-nowhere" },
		func(t *Topology) { t.Hold = append(t.Hold, HoldSubscription{Name: t.Push[0].Name, Topic: TopicTick}) },
		func(t *Topology) { t.Topics[0].Publishers = []string{"scoring", "api"} },
		func(t *Topology) { t.Topics[0].Publishers = nil },
	}
	for i, br := range broken {
		top := Spec()
		br(&top)
		if top.Validate() == nil {
			t.Errorf("broken topology %d validates", i)
		}
	}
}

func TestNames(t *testing.T) {
	for key, topic := range map[string]string{
		events.KeyIngested: TopicIngested, events.KeySessionCompleted: TopicSessionCompleted, events.KeyScored: TopicScored,
		events.KeySessionScored: TopicSessionScored, events.KeyRefresh: TopicRefresh, events.KeyInboxReceived: TopicInboxReceived,
	} {
		if got, ok := TopicFor(key); !ok || got != topic {
			t.Errorf("TopicFor(%s) = %s %v, want %s", key, got, ok, topic)
		}
		if got, ok := KeyOfPath(PushPath(key)); !ok || got != key {
			t.Errorf("KeyOfPath(%s) = %s %v", PushPath(key), got, ok)
		}
	}
	for _, k := range []string{"", "tick", "wait", "Ingested", "ingested "} {
		if _, ok := TopicFor(k); ok {
			t.Errorf("TopicFor(%q) has a topic", k)
		}
	}
	for _, p := range []string{PathWait, PathTick, "/push/", "/push/tick", "/v1/sync", "/push/ingested/x"} {
		if _, ok := KeyOfPath(p); ok {
			t.Errorf("KeyOfPath(%q) has a key", p)
		}
	}
	var ordered []string
	for _, x := range Spec().Topics {
		if Ordered(x.Name) {
			ordered = append(ordered, x.Name)
		}
	}
	if strings.Join(ordered, ",") != TopicIngested+","+TopicSessionCompleted {
		t.Errorf("ordered topics %v: only scoring's two", ordered)
	}
	if d, _ := DLQTopic(events.ConsumerScoring); d != TopicDLQScoring {
		t.Error(d)
	}
	if d, _ := DLQTopic(events.ConsumerNotify); d != TopicDLQNotify {
		t.Error(d)
	}
	if _, ok := DLQTopic("api"); ok {
		t.Error("api is no consumer")
	}
	if got := strings.Join(PushPaths(ServiceScoring), " "); got != "/push/ingested /push/session.completed" {
		t.Errorf("scoring serves %s", got)
	}
	if got := strings.Join(PushPaths(ServiceNotify), " "); got != "/push/scored /push/session.scored /push/refresh /push/wait" {
		t.Errorf("notify serves %s", got)
	}
	if got := strings.Join(PushPaths(ServiceIngest), " "); got != "/push/inbox.received" {
		t.Errorf("ingest serves %s", got)
	}
	if len(PushPaths(ServiceAPI)) != 0 {
		t.Error("api serves the tick only")
	}
	if d, _ := DLQTopic(events.ConsumerIngest); d != TopicDLQIngest {
		t.Error(d)
	}
}
