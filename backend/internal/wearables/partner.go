package wearables

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
)

// PartnerBodyLimit bounds the webhook envelope before verification.
const (
	PartnerBodyLimit = 512 << 10
	maxFITBytes      = 16 << 20
	maxFITMessages   = 500000
)

// Overnight contains typed RMSSD and optional sleep and resting pulse measurements.
type Overnight struct {
	WakeAt time.Time `json:"wake_at"`
	RMSSD  *float64  `json:"rmssd_ms,omitempty"`
	RHR    *float64  `json:"rhr,omitempty"`
	Deep   *float64  `json:"sws_min,omitempty"`
	REM    *float64  `json:"rem_min,omitempty"`
}

// WeighIn is a timestamped body mass in kilograms.
type WeighIn struct {
	At   time.Time `json:"at"`
	Mass float64   `json:"kg"`
}

// ActivitySample is a timestamped active-energy report from a partner.
type ActivitySample struct {
	At   time.Time `json:"at"`
	Kcal float64   `json:"active_kcal"`
}

// Delivery is a bounded partner update for a verified account reference.
type Delivery struct {
	User      string           `json:"provider_user"`
	RemoteID  string           `json:"remote_id,omitempty"`
	OffsetSec int              `json:"tz_offset_sec,omitempty"`
	Workouts  []Workout        `json:"workouts,omitempty"`
	Nights    []Overnight      `json:"nights,omitempty"`
	Weights   []WeighIn        `json:"weights,omitempty"`
	Activity  []ActivitySample `json:"activity,omitempty"`
}

func validReference(ref string) bool {
	return ref != "" && len(ref) <= 192 && !strings.ContainsAny(ref, "\x00\r\n")
}

// Signed verifies an HMAC over the original webhook bytes.
func Signed(body []byte, signature, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}
	signature = strings.TrimPrefix(strings.TrimSpace(signature), "sha256=")
	got, err := hex.DecodeString(signature)
	if err != nil {
		got, err = base64.StdEncoding.DecodeString(signature)
	}
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

type providerID string

func (id *providerID) UnmarshalJSON(b []byte) error {
	var str string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
	} else {
		if _, err := strconv.ParseUint(string(b), 10, 64); err != nil {
			return err
		}
		str = string(b)
	}
	*id = providerID(str)
	return nil
}

type garminRecord struct {
	User        string     `json:"userId"`
	ID          providerID `json:"activityId"`
	Summary     string     `json:"summaryId"`
	Parent      providerID `json:"parentActivityId"`
	Seconds     int64      `json:"startTimeInSeconds"`
	Offset      int        `json:"startTimeOffsetInSeconds"`
	Duration    int        `json:"durationInSeconds"`
	Sport       string     `json:"activityType"`
	Kcal        float64    `json:"activeKilocalories"`
	Distance    float64    `json:"distanceInMeters"`
	AvgHR       float64    `json:"averageHeartRateInBeatsPerMinute"`
	MaxHR       float64    `json:"maxHeartRateInBeatsPerMinute"`
	Manual      bool       `json:"manual"`
	Callback    string     `json:"callbackURL"`
	WeightGrams float64    `json:"weightInGrams"`
	RMSSD       *float64   `json:"lastNightAvg"`
	RHR         *float64   `json:"restingHeartRateInBeatsPerMinute"`
	DeepSeconds *float64   `json:"deepSleepDurationInSeconds"`
	REMSeconds  *float64   `json:"remSleepInSeconds"`
}

func garminDeliveries(body []byte) ([]Delivery, error) {
	var payload map[string][]garminRecord
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	var result []Delivery
	for _, key := range []string{"activities", "activityDetails", "activityFiles", "activityUpdates", "sleeps", "hrv", "bodyComps", "dailies"} {
		for _, r := range payload[key] {
			if !validReference(r.User) {
				continue
			}
			d := Delivery{User: r.User, OffsetSec: r.Offset}
			id := string(r.ID)
			if id == "" {
				id = r.Summary
			}
			start := time.Unix(r.Seconds, 0).UTC()
			switch key {
			case "activities", "activityDetails", "activityFiles", "activityUpdates":
				if !validReference(id) {
					continue
				}
				if r.Callback != "" || key == "activityFiles" {
					// Resolve only by the partner's ID through the configured authenticated connector.
					d.RemoteID = id
				}
				w := Workout{ExternalID: id, ParentID: string(r.Parent), Provider: GarminJSON, Source: "Garmin",
					Start: start, End: start.Add(time.Duration(r.Duration) * time.Second), OffsetSec: r.Offset, DurationSec: float64(r.Duration),
					Sport: Sport(r.Sport), RawKcal: r.Kcal, DistanceM: r.Distance, AvgHR: r.AvgHR, MaxHR: r.MaxHR, UserEntered: r.Manual}
				if r.Seconds > 0 && w.Valid() {
					d.Workouts = []Workout{w}
				}
				if d.RemoteID == "" && len(d.Workouts) == 0 {
					continue
				}
			case "bodyComps":
				if r.Seconds <= 0 || !finiteIn(r.WeightGrams/1000, 25, 350) {
					continue
				}
				d.Weights = []WeighIn{{At: start, Mass: r.WeightGrams / 1000}}
			case "sleeps", "hrv", "dailies":
				if r.Seconds <= 0 {
					continue
				}
				if key == "sleeps" && r.Duration > 0 {
					start = start.Add(time.Duration(r.Duration) * time.Second)
				}
				n := Overnight{WakeAt: start, RMSSD: r.RMSSD, RHR: r.RHR, Deep: minuteValue(r.DeepSeconds), REM: minuteValue(r.REMSeconds)}
				if n.RMSSD == nil && n.RHR == nil && n.Deep == nil && n.REM == nil {
					continue
				}
				d.Nights = []Overnight{n}
			}
			result = append(result, d)
		}
	}
	return result, nil
}

func minuteValue(s *float64) *float64 {
	if s == nil {
		return nil
	}
	m := *s / 60
	return &m
}

// DecodeDelivery accepts the Garmin push/ping envelope and Suunto's notification fields.
func DecodeDelivery(kind string, body []byte) ([]Delivery, error) {
	switch kind {
	case "garmin":
		return garminDeliveries(body)
	case "suunto":
		var p struct {
			User string     `json:"username"`
			ID   providerID `json:"workoutid"`
		}
		if !json.Valid(body) {
			q, err := url.ParseQuery(string(body))
			if err != nil {
				return nil, err
			}
			p.User, p.ID = q.Get("username"), providerID(q.Get("workoutid"))
		} else if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		if !validReference(p.User) || !validReference(string(p.ID)) {
			return nil, errors.New("suunto notification is missing its IDs")
		}
		return []Delivery{{User: p.User, RemoteID: string(p.ID)}}, nil
	default:
		return nil, errors.New("unknown provider")
	}
}

// Fetchers holds operator-configured HTTPS bridges keyed by provider.
type Fetchers map[string]*url.URL

// NewFetchers validates the trusted bridge URLs at boot. Job data never controls the host.
func NewFetchers(garmin, suunto string) (Fetchers, error) {
	f := Fetchers{}
	for kind, base := range map[string]string{"garmin": garmin, "suunto": suunto} {
		if base == "" {
			continue
		}
		u, err := url.Parse(base)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return nil, errors.New("invalid partner fetch URL")
		}
		f[kind] = u
	}
	return f, nil
}

// Fetch resolves a queued partner reference through its configured bridge.
func (f Fetchers) Fetch(ctx context.Context, kind string, d Delivery, token string, transport http.RoundTripper) (Delivery, error) {
	if d.RemoteID == "" {
		return d, nil
	}
	target := f[kind]
	if target == nil || token == "" {
		return d, errors.New("partner connector is not configured")
	}
	u := *target
	q := u.Query()
	q.Set("provider_user", d.User)
	q.Set("workout_id", d.RemoteID)
	u.RawQuery = q.Encode()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return d, err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "application/octet-stream, application/json")
	client := http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		return d, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return d, fmt.Errorf("partner connector returned HTTP %d", resp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, maxFITBytes+1))
	if err != nil {
		return d, err
	}
	if len(content) > maxFITBytes {
		return d, errors.New("partner artifact exceeds limit")
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		var result Delivery
		if err := json.Unmarshal(content, &result); err != nil {
			return d, err
		}
		if result.User == "" && kind == "garmin" {
			deliveries, err := garminDeliveries(content)
			if err != nil {
				return d, err
			}
			for _, item := range deliveries {
				if item.User != d.User {
					continue
				}
				for _, w := range item.Workouts {
					if w.ExternalID == d.RemoteID {
						d.Workouts = append(d.Workouts, w)
					}
				}
				d.Nights = append(d.Nights, item.Nights...)
				d.Weights = append(d.Weights, item.Weights...)
			}
			if len(d.Workouts) == 0 && len(d.Nights) == 0 && len(d.Weights) == 0 {
				return d, errors.New("partner data not ready")
			}
			return d, nil
		}
		if result.User != d.User || len(result.Workouts) != 0 {
			return d, errors.New("unexpected connector response")
		}
		d.Nights = append(d.Nights, result.Nights...)
		d.Weights = append(d.Weights, result.Weights...)
		d.Activity = append(d.Activity, result.Activity...)
		return d, nil
	}
	decoded, err := DecodeFIT(ctx, kind, d.RemoteID, d.OffsetSec, bytes.NewReader(content))
	if err != nil {
		return d, err
	}
	d.Workouts = append(d.Workouts, decoded...)
	return d, nil
}

type powerSample struct {
	at    time.Time
	watts uint16
}

type fitEvents struct {
	fields        map[[2]byte]*mesgdef.FieldDescription
	sessionExtras []map[string]float64
	power         []powerSample
	count         int
	rr            bool
	sessions      []*mesgdef.Session
}

func (e *fitEvents) OnMesg(m proto.Message) {
	e.count++
	if e.count > maxFITMessages {
		return
	}
	switch m.Num {
	case typedef.MesgNumFieldDescription:
		desc := mesgdef.NewFieldDescription(&m)
		if len(e.fields) < 256 {
			e.fields[[2]byte{desc.DeveloperDataIndex, desc.FieldDefinitionNumber}] = desc
		}
	case typedef.MesgNumSession:
		if len(e.sessions) < 100 {
			e.sessions = append(e.sessions, mesgdef.NewSession(&m))
			e.sessionExtras = append(e.sessionExtras, sessionMeasurements(e.fields, m.DeveloperFields))
		}
	case typedef.MesgNumRecord:
		r := mesgdef.NewRecord(&m)
		if r.Power != ^uint16(0) && r.Power <= 2500 {
			e.power = append(e.power, powerSample{r.Timestamp, r.Power})
		}
	case typedef.MesgNumHrv:
		for _, rr := range mesgdef.NewHrv(&m).Time {
			if rr >= 250 && rr <= 2500 {
				e.rr = true
				break
			}
		}
	}
}

// DecodeFIT extracts bounded session, power and R-R observations from a FIT file.
func DecodeFIT(ctx context.Context, src, id string, offset int, r io.Reader) ([]Workout, error) {
	e := &fitEvents{fields: map[[2]byte]*mesgdef.FieldDescription{}}
	dec := decoder.New(io.LimitReader(r, maxFITBytes), decoder.WithMesgListener(e), decoder.WithBroadcastOnly())
	if _, err := dec.DecodeWithContext(ctx); err != nil {
		return nil, err
	}
	if e.count > maxFITMessages || len(e.sessions) == 0 {
		return nil, errors.New("invalid FIT session count")
	}
	var out []Workout
	provider := GarminFIT
	if src == "suunto" {
		provider = SuuntoFIT
	}
	for i, s := range e.sessions {
		hrs := float64(s.AvgHeartRate)
		if s.AvgHeartRate == 255 {
			hrs = 0
		}
		maxHR := float64(s.MaxHeartRate)
		if s.MaxHeartRate == 255 {
			maxHR = 0
		}
		start := s.StartTime.UTC()
		end := s.Timestamp.UTC()
		elapsed := validU32(s.TotalElapsedTime) / 1000
		if elapsed > 0 {
			end = start.Add(time.Duration(elapsed * float64(time.Second)))
		}
		w := Workout{Provider: provider, ExternalID: id, Start: start, End: end, OffsetSec: offset, Sport: Sport(s.Sport.String()),
			DurationSec: validU32(s.TotalTimerTime) / 1000, RawKcal: validU16(s.TotalCalories),
			DistanceM: validU32(s.TotalDistance) / 100, AvgHR: hrs, MaxHR: maxHR, WorkKJ: validU32(s.TotalWork) / 1000,
			FTP: validU16(s.ThresholdPower), NormalizedPower: validU16(s.NormalizedPower), HasRR: e.rr}
		if provider == SuuntoFIT {
			w.SuuntoPlus = e.sessionExtras[i]
		}
		if w.WorkKJ == 0 && w.Sport == "cycling" {
			w.WorkKJ = powerWork(e.power, w.Start, w.End)
		}
		if len(e.sessions) > 1 {
			w.ParentID, w.ExternalID = id, fmt.Sprintf("%s:%d", id, i+1)
		}
		if w.Valid() {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("FIT contains no valid workout")
	}
	return out, nil
}

func sessionMeasurements(descriptions map[[2]byte]*mesgdef.FieldDescription, fields []proto.DeveloperField) map[string]float64 {
	out := map[string]float64{}
	for _, field := range fields {
		d := descriptions[[2]byte{field.DeveloperDataIndex, field.Num}]
		if d == nil || len(d.FieldName) == 0 || !field.Value.Valid(d.FitBaseTypeId) {
			continue
		}
		label := strings.Join(strings.Fields(strings.ToLower(strings.ReplaceAll(d.FieldName[0], "_", " "))), "_")
		switch label {
		case "average_power", "running_power", "normalized_power", "form_power", "vertical_oscillation",
			"ground_contact_time", "leg_spring_stiffness", "efficiency":
		default:
			continue
		}
		var n float64
		switch v := field.Value.Any().(type) {
		case uint8:
			n = float64(v)
		case uint16:
			n = float64(v)
		case uint32:
			n = float64(v)
		case int16:
			n = float64(v)
		case int32:
			n = float64(v)
		case float32:
			n = float64(v)
		case float64:
			n = v
		default:
			continue
		}
		if d.Scale != 0 && d.Scale != 255 {
			n /= float64(d.Scale)
		}
		if d.Offset != 127 {
			n -= float64(d.Offset)
		}
		if finiteIn(n, 0, 100000) {
			out[label] = n
		}
	}
	return out
}

// Integration accepts at most five seconds between adjacent measured power samples.
func powerWork(samples []powerSample, start, end time.Time) float64 {
	var previous powerSample
	var joules float64
	for _, r := range samples {
		if previous.at.Before(start) || r.at.After(end) || !previous.at.Before(r.at) {
			previous = r
			continue
		}
		delta := r.at.Sub(previous.at).Seconds()
		if delta <= 5 {
			joules += float64(previous.watts) * delta
		}
		previous = r
	}
	return joules / 1000
}

func validU32(n uint32) float64 {
	if n == ^uint32(0) {
		return 0
	}
	return float64(n)
}
func validU16(n uint16) float64 {
	if n == ^uint16(0) {
		return 0
	}
	return float64(n)
}

// TenMinuteActivity groups partner samples by UTC slot without multiplying duplicate deliveries.
func TenMinuteActivity(samples []ActivitySample) map[time.Time]float64 {
	seen := make(map[time.Time]bool, len(samples))
	out := map[time.Time]float64{}
	for _, s := range samples {
		key := s.At.UTC()
		if key.IsZero() || seen[key] || !finiteIn(s.Kcal, 0, 1000) {
			continue
		}
		seen[key] = true
		out[key.Truncate(10*time.Minute)] += s.Kcal
	}
	return out
}
