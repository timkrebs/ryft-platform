package eventbus

import (
	"strings"
	"testing"

	"github.com/timkrebs/ryft/internal/config"
)

// The four releases of the cutover (D102), per service.
func TestPlanThroughTheCutover(t *testing.T) {
	ingest := Role{Name: "ingest", Publishes: true}
	scoring := Role{Name: "scoring", Publishes: true, Consumes: true, OwnStream: true}
	notify := Role{Name: "notify", Consumes: true}
	cfg := func(backend, consume string) config.Config {
		return config.Config{EventsBackend: backend, EventsConsume: strings.Split(consume, ",")}
	}
	for _, c := range []struct {
		release string
		cfg     config.Config
		role    Role
		want    string // open | publish | consume
	}{
		{"R1", cfg("nats", "nats"), ingest, "nats|nats|"},
		{"R1", cfg("nats", "nats"), scoring, "nats|nats|nats"},
		{"R2", cfg("nats", "nats,amqp"), scoring, "nats,amqp|nats|nats,amqp"},
		{"R2", cfg("nats", "nats,amqp"), notify, "nats,amqp|nats|nats,amqp"},
		{"R2", cfg("nats", "nats"), ingest, "nats|nats|"},
		{"R3", cfg("amqp", "nats,amqp"), ingest, "amqp|amqp|"},
		{"R3", cfg("amqp", "nats,amqp"), scoring, "nats,amqp|amqp|nats,amqp"},
		{"R4", cfg("amqp", "amqp"), scoring, "amqp|amqp|amqp"},
		{"R4", cfg("amqp", "amqp"), notify, "amqp|amqp|amqp"},
		// notify publishes nothing: consuming only RabbitMQ opens only RabbitMQ.
		{"R4", cfg("nats", "amqp"), notify, "amqp|amqp|amqp"},
	} {
		p := PlanFor(c.cfg, c.role)
		got := strings.Join(p.Open, ",") + "|" + p.Publish + "|" + strings.Join(p.Consume, ",")
		if got != c.want {
			t.Errorf("%s %s: %s, want %s", c.release, c.role.Name, got, c.want)
		}
	}
}
