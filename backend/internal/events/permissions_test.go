package events

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Integration test, opt-in: RYFT_TEST_NATS_URL points at a NATS running
// deploy/k8s/base/nats/nats.conf with the compose dev passwords (make up):
//
//	RYFT_TEST_NATS_URL=nats://localhost:4222 go test ./internal/events/ -run Permissions
func natsURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("RYFT_TEST_NATS_URL")
	if u == "" {
		t.Skip("set RYFT_TEST_NATS_URL to a NATS running deploy/k8s/base/nats/nats.conf (make up)")
	}
	return u
}

func devPassword(service string) string {
	if p := os.Getenv("NATS_PASSWORD_" + strings.ToUpper(service)); p != "" {
		return p
	}
	return "dev-" + service + "-nats-password"
}

// violations connects as service and records the server's asynchronous errors.
type violations struct {
	mu   sync.Mutex
	errs []string
}

func (v *violations) saw(substr string) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v.mu.Lock()
		for _, e := range v.errs {
			if strings.Contains(e, substr) {
				v.mu.Unlock()
				return true
			}
		}
		v.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func connectAs(t *testing.T, service string) (*nats.Conn, *violations) {
	t.Helper()
	v := &violations{}
	nc, err := nats.Connect(natsURL(t), nats.UserInfo(service, devPassword(service)),
		nats.CustomInboxPrefix("_INBOX_"+service),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			v.mu.Lock()
			v.errs = append(v.errs, err.Error())
			v.mu.Unlock()
		}))
	if err != nil {
		t.Fatalf("connect as %s: %v", service, err)
	}
	t.Cleanup(nc.Close)
	return nc, v
}

func TestNatsPermissionsAuthentication(t *testing.T) {
	u := natsURL(t)
	if nc, err := nats.Connect(u); err == nil {
		nc.Close()
		t.Fatal("anonymous clients must be refused")
	}
	if nc, err := nats.Connect(u, nats.UserInfo("ingest", "wrong")); err == nil {
		nc.Close()
		t.Fatal("a wrong password must be refused")
	}
	if nc, err := nats.Connect(u, nats.UserInfo("scoring", devPassword("ingest"))); err == nil {
		nc.Close()
		t.Fatal("one service's password must not open another's user")
	}
}

func TestNatsPermissionsPublishersOnlyPublish(t *testing.T) {
	ctx := context.Background()
	scoring, err := Connect(ctx, natsURL(t), Options{Name: "scoring", User: "scoring", Password: devPassword("scoring"), OwnStream: true})
	if err != nil {
		t.Fatalf("scoring owns the stream: %v", err)
	}
	scoring.Close()

	for _, svc := range []string{"ingest", "api"} {
		nc, v := connectAs(t, svc)
		js, err := jetstream.New(nc)
		if err != nil {
			t.Fatal(err)
		}
		short, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err = js.CreateOrUpdateStream(short, jetstream.StreamConfig{Name: "EVIL", Subjects: []string{"evil.>"}})
		cancel()
		if err == nil {
			t.Errorf("%s must not create streams", svc)
		}
		short, cancel = context.WithTimeout(ctx, 2*time.Second)
		_, err = js.Publish(short, "ryft.other", []byte("x"))
		cancel()
		if err == nil {
			t.Errorf("%s may publish only ryft.ingested", svc)
		}
		if _, err := nc.SubscribeSync(SubjectIngested); err != nil {
			t.Fatal(err)
		}
		if !v.saw(`Subscription to "ryft.ingested"`) {
			t.Errorf("%s must not read the event subject", svc)
		}
		if _, err := nc.SubscribeSync("_INBOX_scoring.>"); err != nil {
			t.Fatal(err)
		}
		if !v.saw(`Subscription to "_INBOX_scoring.>"`) {
			t.Errorf("%s must not read scoring's replies", svc)
		}
		if err := nc.Publish("$JS.API.CONSUMER.MSG.NEXT.RYFT.scoring", nil); err != nil {
			t.Fatal(err)
		}
		if !v.saw(`Publish to "$JS.API.CONSUMER.MSG.NEXT.RYFT.scoring"`) {
			t.Errorf("%s must not pull scoring's messages", svc)
		}
	}
}

func TestNatsPermissionsScoringOnlyConsumes(t *testing.T) {
	nc, v := connectAs(t, "scoring")
	if err := nc.Publish(SubjectIngested, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if !v.saw(`Publish to "ryft.ingested"`) {
		t.Error("scoring must not publish events")
	}
	js, _ := jetstream.New(nc)
	short, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := js.DeleteStream(short, StreamName); err == nil || errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Errorf("scoring must not delete the stream: %v", err)
	}
}
