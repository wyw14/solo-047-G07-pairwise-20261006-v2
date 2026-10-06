package healthscore

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-103/internal/route"
	"github.com/wyw14/cry-103/internal/telemetry"
)

// Thresholds bound each signal's scoring curve. Values at or better than
// Nominal score 100; values at or worse than Limit score 0, linear in between.
type Thresholds struct {
	TemperatureNominalC  float64
	TemperatureLimitC    float64
	OpticalNominalDBM    float64
	OpticalLimitDBM      float64
	ErrorRateNominal     float64
	ErrorRateLimit       float64
	HeartbeatTimeout     time.Duration
	HeartbeatLimitFactor float64
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		TemperatureNominalC:  45,
		TemperatureLimitC:    85,
		OpticalNominalDBM:    -2,
		OpticalLimitDBM:      -28,
		ErrorRateNominal:     1e-9,
		ErrorRateLimit:       1e-3,
		HeartbeatTimeout:     10 * time.Second,
		HeartbeatLimitFactor: 3,
	}
}

func (t Thresholds) Validate() error {
	if t.TemperatureLimitC <= t.TemperatureNominalC {
		return errors.New("temperature limit must exceed nominal")
	}
	if t.OpticalLimitDBM >= t.OpticalNominalDBM {
		return errors.New("optical limit must be below nominal")
	}
	if t.ErrorRateLimit <= t.ErrorRateNominal || t.ErrorRateNominal <= 0 {
		return errors.New("error-rate limits must be positive and ordered")
	}
	if t.HeartbeatTimeout <= 0 || t.HeartbeatLimitFactor <= 1 {
		return errors.New("heartbeat timeout and limit factor greater than one are required")
	}
	return nil
}

var componentWeights = map[Component]float64{
	ComponentTemperature:  0.25,
	ComponentOpticalPower: 0.25,
	ComponentErrorRate:    0.20,
	ComponentHeartbeatAge: 0.15,
	ComponentProtection:   0.15,
}

const (
	gradeHealthyFloor  = 80.0
	gradeDegradedFloor = 50.0
	trendEpsilon       = 2.0
	historyDepth       = 8
)

// LineLookup resolves the route and its telemetry assets for one scored line.
type LineLookup interface {
	RouteSnapshot(routeID string) (route.Snapshot, error)
	Assets(routeID string) []string
}

type Scorer struct {
	mu         sync.RWMutex
	samples    *telemetry.Repository
	lines      LineLookup
	thresholds Thresholds
	now        func() time.Time
	history    map[string][]float64
}

func NewScorer(samples *telemetry.Repository, lines LineLookup, thresholds Thresholds, now func() time.Time) (*Scorer, error) {
	if samples == nil || lines == nil || now == nil {
		return nil, errors.New("sample repository, line lookup and clock are required")
	}
	if err := thresholds.Validate(); err != nil {
		return nil, err
	}
	return &Scorer{samples: samples, lines: lines, thresholds: thresholds, now: now, history: make(map[string][]float64)}, nil
}

// Assess scores one line. When any signal lacks samples the grade degrades to
// unknown instead of asserting health.
func (s *Scorer) Assess(routeID string) (Assessment, error) {
	snapshot, err := s.lines.RouteSnapshot(routeID)
	if err != nil {
		return Assessment{}, err
	}
	assets := s.lines.Assets(routeID)
	at := s.now().UTC()
	components := []ComponentScore{
		s.scoreTemperature(assets),
		s.scoreOpticalPower(assets),
		s.scoreErrorRate(assets),
		s.scoreHeartbeat(assets, at),
		scoreProtection(snapshot),
	}
	assessment := Assessment{
		RouteID: routeID, SpanID: snapshot.SpanID, Components: components, AssessedAt: at,
	}
	var missing []Component
	known := make([]ComponentScore, 0, len(components))
	for _, component := range components {
		if component.State == StateUnknown {
			missing = append(missing, component.Component)
			continue
		}
		known = append(known, component)
	}
	if len(missing) > 0 {
		assessment.Grade = GradeUnknown
		assessment.Trend = TrendUnknown
		assessment.Missing = missing
		return assessment, nil
	}
	total := 0.0
	for _, component := range known {
		total += *component.Score * component.Weight
	}
	assessment.Score = &total
	switch {
	case total >= gradeHealthyFloor:
		assessment.Grade = GradeHealthy
	case total >= gradeDegradedFloor:
		assessment.Grade = GradeDegraded
	default:
		assessment.Grade = GradeCritical
	}
	// A critical component caps the line at degraded: one severely degraded
	// signal must not be hidden by healthy scores elsewhere.
	for _, component := range known {
		if component.State == StateCritical && assessment.Grade == GradeHealthy {
			assessment.Grade = GradeDegraded
		}
	}
	assessment.Trend = s.recordAndTrend(routeID, total)
	if assessment.Grade != GradeHealthy {
		assessment.Dominant = dominant(known)
	}
	return assessment, nil
}

func (s *Scorer) recordAndTrend(routeID string, score float64) Trend {
	s.mu.Lock()
	defer s.mu.Unlock()
	series := s.history[routeID]
	trend := TrendUnknown
	if len(series) > 0 {
		previous := series[len(series)-1]
		switch {
		case score < previous-trendEpsilon:
			trend = TrendDegrading
		case score > previous+trendEpsilon:
			trend = TrendImproving
		default:
			trend = TrendStable
		}
	}
	series = append(series, score)
	if len(series) > historyDepth {
		series = series[len(series)-historyDepth:]
	}
	s.history[routeID] = series
	return trend
}

// dominant returns the component with the largest weighted deduction — the
// evidence that contributes most to the line's score loss.
func dominant(components []ComponentScore) *ComponentScore {
	ordered := make([]ComponentScore, len(components))
	copy(ordered, components)
	sort.SliceStable(ordered, func(i, j int) bool {
		return deduction(ordered[i]) > deduction(ordered[j])
	})
	if len(ordered) == 0 || deduction(ordered[0]) <= 0 {
		return nil
	}
	top := ordered[0]
	return &top
}

func deduction(component ComponentScore) float64 {
	if component.Score == nil {
		return 0
	}
	return (100 - *component.Score) * component.Weight
}

func (s *Scorer) latest(assets []string, kind telemetry.Kind) (telemetry.Sample, bool) {
	var latest telemetry.Sample
	found := false
	for _, asset := range assets {
		sample, ok := s.samples.Latest(asset, kind)
		if !ok {
			continue
		}
		if !found || sample.ObservedAt.After(latest.ObservedAt) ||
			(sample.ObservedAt.Equal(latest.ObservedAt) && sample.Sequence > latest.Sequence) {
			latest, found = sample, true
		}
	}
	return latest, found
}

func (s *Scorer) scoreTemperature(assets []string) ComponentScore {
	component := ComponentScore{Component: ComponentTemperature, State: StateUnknown, Weight: componentWeights[ComponentTemperature]}
	sample, ok := s.latest(assets, telemetry.KindTemperature)
	if !ok {
		component.Evidence = "no temperature samples"
		return component
	}
	score := linearScore(sample.Value, s.thresholds.TemperatureNominalC, s.thresholds.TemperatureLimitC)
	return finishSample(component, score, sample, "C",
		fmt.Sprintf("repeater temperature %.1fC (nominal %.0fC, limit %.0fC)", sample.Value, s.thresholds.TemperatureNominalC, s.thresholds.TemperatureLimitC))
}

func (s *Scorer) scoreOpticalPower(assets []string) ComponentScore {
	component := ComponentScore{Component: ComponentOpticalPower, State: StateUnknown, Weight: componentWeights[ComponentOpticalPower]}
	sample, ok := s.latest(assets, telemetry.KindOpticalPower)
	if !ok {
		component.Evidence = "no optical power samples"
		return component
	}
	// Higher receive power is better: invert the linear curve.
	score := linearScore(-sample.Value, -s.thresholds.OpticalNominalDBM, -s.thresholds.OpticalLimitDBM)
	return finishSample(component, score, sample, "dBm",
		fmt.Sprintf("receive power %.1f dBm (nominal %.0f dBm, limit %.0f dBm)", sample.Value, s.thresholds.OpticalNominalDBM, s.thresholds.OpticalLimitDBM))
}

func (s *Scorer) scoreErrorRate(assets []string) ComponentScore {
	component := ComponentScore{Component: ComponentErrorRate, State: StateUnknown, Weight: componentWeights[ComponentErrorRate]}
	sample, ok := s.latest(assets, telemetry.KindErrorRate)
	if !ok {
		component.Evidence = "no error-rate samples"
		return component
	}
	score := logScore(sample.Value, s.thresholds.ErrorRateNominal, s.thresholds.ErrorRateLimit)
	return finishSample(component, score, sample, "BER",
		fmt.Sprintf("bit error rate %.2e (nominal %.0e, limit %.0e)", sample.Value, s.thresholds.ErrorRateNominal, s.thresholds.ErrorRateLimit))
}

func (s *Scorer) scoreHeartbeat(assets []string, at time.Time) ComponentScore {
	component := ComponentScore{Component: ComponentHeartbeatAge, State: StateUnknown, Weight: componentWeights[ComponentHeartbeatAge]}
	sample, ok := s.latest(assets, telemetry.KindHeartbeat)
	if !ok {
		component.Evidence = "no heartbeat samples"
		return component
	}
	age := at.Sub(sample.ObservedAt.UTC())
	if age < 0 {
		age = 0
	}
	timeout := s.thresholds.HeartbeatTimeout
	score := linearScore(float64(age), float64(timeout), float64(timeout)*s.thresholds.HeartbeatLimitFactor)
	component.State = stateFor(score)
	component.Score = &score
	component.AssetID = sample.AssetID
	observed := sample.ObservedAt.UTC()
	component.ObservedAt = &observed
	component.Evidence = fmt.Sprintf("heartbeat age %s (timeout %s)", age.Round(time.Millisecond), timeout)
	return component
}

func scoreProtection(snapshot route.Snapshot) ComponentScore {
	component := ComponentScore{Component: ComponentProtection, Weight: componentWeights[ComponentProtection]}
	var score float64
	switch snapshot.State {
	case route.StatePrimary:
		score = 100
		component.Evidence = "traffic on primary path"
	case route.StateDraining, route.StateRestoring:
		score = 60
		component.Evidence = fmt.Sprintf("path transition in progress (%s)", snapshot.State)
	case route.StateProtectionActive:
		score = 30
		component.Evidence = "traffic carried on protection path"
	default:
		component.State = StateUnknown
		component.Evidence = fmt.Sprintf("route state %q not scored", snapshot.State)
		return component
	}
	component.State = stateFor(score)
	component.Score = &score
	return component
}

func finishSample(component ComponentScore, score float64, sample telemetry.Sample, unit, evidence string) ComponentScore {
	component.State = stateFor(score)
	component.Score = &score
	component.AssetID = sample.AssetID
	value := sample.Value
	component.Value = &value
	component.Unit = unit
	observed := sample.ObservedAt.UTC()
	component.ObservedAt = &observed
	component.Evidence = evidence
	return component
}

func stateFor(score float64) ComponentState {
	switch {
	case score >= gradeHealthyFloor:
		return StateOK
	case score >= gradeDegradedFloor:
		return StateWarn
	default:
		return StateCritical
	}
}

// linearScore maps value in [nominal, limit] onto [100, 0], clamped.
func linearScore(value, nominal, limit float64) float64 {
	if value <= nominal {
		return 100
	}
	if value >= limit {
		return 0
	}
	return 100 * (limit - value) / (limit - nominal)
}

// logScore is linearScore on a log10 scale, for quantities like BER that
// span orders of magnitude.
func logScore(value, nominal, limit float64) float64 {
	if value <= 0 {
		return 100
	}
	return linearScore(math.Log10(value), math.Log10(nominal), math.Log10(limit))
}
