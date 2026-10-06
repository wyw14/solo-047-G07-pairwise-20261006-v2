package linescore

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-103/internal/route"
	"github.com/wyw14/cry-103/internal/telemetry"
	"github.com/wyw14/cry-103/internal/topology"
)

// maxKnownScores bounds the per-route score history used for trend detection.
const maxKnownScores = 32

// Service computes line-health assessments for routes from telemetry
// evidence and the route's current protection path.
type Service struct {
	mu         sync.Mutex
	routes     map[string]*route.Route
	repository *telemetry.Repository
	age        *telemetry.AgeEvaluator
	now        func() time.Time
	config     Config
	assets     map[string][]string
	history    map[string][]knownScore
}

type knownScore struct {
	score float64
	at    time.Time
}

func NewService(routes map[string]*route.Route, repository *telemetry.Repository, evaluator *telemetry.AgeEvaluator, now func() time.Time, config Config) (*Service, error) {
	if routes == nil || repository == nil || evaluator == nil || now == nil {
		return nil, errors.New("routes, telemetry repository, age evaluator and clock are required")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	config.Weights = config.Weights.normalized()
	return &Service{
		routes: routes, repository: repository, age: evaluator, now: now, config: config,
		assets: make(map[string][]string), history: make(map[string][]knownScore),
	}, nil
}

// Register binds telemetry assets (repeaters) to a route for evidence
// collection. A route with no registered assets always assesses as unknown.
func (s *Service) Register(routeID string, assetIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assets[routeID] = append([]string(nil), assetIDs...)
}

// Assess evaluates the current line health of one route.
func (s *Service) Assess(routeID string) (Assessment, error) {
	s.mu.Lock()
	target := s.routes[routeID]
	s.mu.Unlock()
	if target == nil {
		return Assessment{}, fmt.Errorf("route %s not found", routeID)
	}
	snapshot := target.Snapshot()
	now := s.now().UTC()
	components := s.evaluateComponents(routeID, snapshot, now)
	status, score := combine(components)
	return Assessment{
		RouteID: routeID, SpanID: snapshot.SpanID, Status: status, Score: score,
		Trend:      s.trendFor(routeID, score, now),
		Dominant:   dominantEvidence(components),
		Components: components, AssessedAt: now,
	}, nil
}

// AssessSpan evaluates every route serving a span, for maintenance views.
func (s *Service) AssessSpan(spanID string) ([]Assessment, error) {
	s.mu.Lock()
	routeIDs := make([]string, 0, len(s.routes))
	for id, target := range s.routes {
		if target.Snapshot().SpanID == spanID {
			routeIDs = append(routeIDs, id)
		}
	}
	s.mu.Unlock()
	sort.Strings(routeIDs)
	assessments := make([]Assessment, 0, len(routeIDs))
	for _, id := range routeIDs {
		assessment, err := s.Assess(id)
		if err != nil {
			return nil, err
		}
		assessments = append(assessments, assessment)
	}
	return assessments, nil
}

func (s *Service) evaluateComponents(routeID string, snapshot route.Snapshot, now time.Time) []Evidence {
	s.mu.Lock()
	assets := append([]string(nil), s.assets[routeID]...)
	s.mu.Unlock()
	sort.Strings(assets)
	evidence := make([]Evidence, 0, len(componentOrder))
	for _, component := range componentOrder {
		evidence = append(evidence, s.componentEvidence(component, assets, snapshot, now))
	}
	return evidence
}

func (s *Service) componentEvidence(component Component, assets []string, snapshot route.Snapshot, now time.Time) Evidence {
	switch component {
	case ComponentTemperature:
		return s.temperatureEvidence(assets, now)
	case ComponentOpticalPower:
		return s.opticalEvidence(assets, now)
	case ComponentErrorRate:
		return s.errorRateEvidence(assets, now)
	case ComponentHeartbeatAge:
		return s.heartbeatEvidence(assets, now)
	default:
		return s.protectionEvidence(snapshot)
	}
}

func (s *Service) temperatureEvidence(assets []string, now time.Time) Evidence {
	weight := s.config.Weights.Temperature
	samples := s.freshSamples(assets, telemetry.KindTemperature, now)
	if len(samples) == 0 {
		return unknownEvidence(ComponentTemperature, weight, "no fresh temperature samples from registered repeaters")
	}
	worst := samples[0]
	for _, sample := range samples[1:] {
		if sample.Value > worst.Value {
			worst = sample
		}
	}
	thresholds := s.config.Thresholds
	score := rampScore(worst.Value, thresholds.TemperatureWarnC, thresholds.TemperatureTripC)
	summary := fmt.Sprintf("repeater %s temperature %.1fC (warn %.1fC, trip %.1fC)",
		worst.AssetID, worst.Value, thresholds.TemperatureWarnC, thresholds.TemperatureTripC)
	return scoredEvidence(ComponentTemperature, weight, score, summary, &worst)
}

func (s *Service) opticalEvidence(assets []string, now time.Time) Evidence {
	weight := s.config.Weights.OpticalPower
	samples := s.freshSamples(assets, telemetry.KindOpticalPower, now)
	if len(samples) == 0 {
		return unknownEvidence(ComponentOpticalPower, weight, "no fresh optical power samples from registered repeaters")
	}
	worst := samples[0]
	for _, sample := range samples[1:] {
		if sample.Value < worst.Value {
			worst = sample
		}
	}
	thresholds := s.config.Thresholds
	score := rampScoreDescending(worst.Value, thresholds.OpticalGoodDBm, thresholds.OpticalDarkDBm)
	summary := fmt.Sprintf("repeater %s optical power %.2f dBm (good >= %.1f dBm, dark <= %.1f dBm)",
		worst.AssetID, worst.Value, thresholds.OpticalGoodDBm, thresholds.OpticalDarkDBm)
	return scoredEvidence(ComponentOpticalPower, weight, score, summary, &worst)
}

func (s *Service) errorRateEvidence(assets []string, now time.Time) Evidence {
	weight := s.config.Weights.ErrorRate
	samples := s.freshSamples(assets, telemetry.KindErrorRate, now)
	if len(samples) == 0 {
		return unknownEvidence(ComponentErrorRate, weight, "no fresh error rate samples from registered repeaters")
	}
	worst := samples[0]
	for _, sample := range samples[1:] {
		if sample.Value > worst.Value {
			worst = sample
		}
	}
	thresholds := s.config.Thresholds
	score := logRampScore(worst.Value, thresholds.ErrorRateGood, thresholds.ErrorRateBad)
	summary := fmt.Sprintf("repeater %s error rate %.2e (good <= %.0e, bad >= %.0e)",
		worst.AssetID, worst.Value, thresholds.ErrorRateGood, thresholds.ErrorRateBad)
	return scoredEvidence(ComponentErrorRate, weight, score, summary, &worst)
}

func (s *Service) heartbeatEvidence(assets []string, now time.Time) Evidence {
	weight := s.config.Weights.HeartbeatAge
	samples := s.freshSamples(assets, telemetry.KindHeartbeat, now)
	if len(samples) == 0 {
		return unknownEvidence(ComponentHeartbeatAge, weight, "no fresh heartbeat samples from registered repeaters")
	}
	latest := samples[0]
	for _, sample := range samples[1:] {
		if sample.ObservedAt.After(latest.ObservedAt) {
			latest = sample
		}
	}
	timeout := s.config.Thresholds.HeartbeatTimeout
	age := s.age.Age(latest.ObservedAt)
	score := rampScore(float64(age), float64(timeout), float64(3*timeout))
	summary := fmt.Sprintf("repeater %s heartbeat age %s (timeout %s)",
		latest.AssetID, age.Round(time.Millisecond), timeout)
	return scoredEvidence(ComponentHeartbeatAge, weight, score, summary, &latest)
}

func (s *Service) protectionEvidence(snapshot route.Snapshot) Evidence {
	weight := s.config.Weights.Protection
	if snapshot.State == route.StatePrimary && snapshot.ActivePath == topology.PathPrimary {
		return scoredEvidence(ComponentProtection, weight, 100, "traffic on primary path", nil)
	}
	score := s.config.Thresholds.ProtectionPathScore
	summary := fmt.Sprintf("route %s with traffic on %s path", snapshot.State, snapshot.ActivePath)
	return scoredEvidence(ComponentProtection, weight, score, summary, nil)
}

// freshSamples returns the latest sample of a kind per asset, dropping
// samples older than SampleMaxAge: stale sampling counts as missing.
func (s *Service) freshSamples(assets []string, kind telemetry.Kind, now time.Time) []telemetry.Sample {
	fresh := make([]telemetry.Sample, 0, len(assets))
	for _, asset := range assets {
		sample, ok := s.repository.Latest(asset, kind)
		if !ok {
			continue
		}
		if now.Sub(sample.ObservedAt.UTC()) > s.config.SampleMaxAge {
			continue
		}
		fresh = append(fresh, sample)
	}
	return fresh
}

func scoredEvidence(component Component, weight, score float64, summary string, sample *telemetry.Sample) Evidence {
	rounded := round1(score)
	evidence := Evidence{
		Component: component, State: stateForScore(rounded),
		Score: &rounded, Weight: round1(weight * 100),
		Loss: round1(weight * (100 - rounded)), Summary: summary,
	}
	if sample != nil {
		evidence.AssetID = sample.AssetID
		observed := sample.ObservedAt.UTC()
		evidence.ObservedAt = &observed
	}
	return evidence
}

// unknownEvidence marks a component whose samples are missing. It carries the
// maximum possible loss for its weight so missing evidence dominates the
// verdict instead of being read as healthy.
func unknownEvidence(component Component, weight float64, summary string) Evidence {
	return Evidence{
		Component: component, State: StateUnknown, Score: nil,
		Weight: round1(weight * 100), Loss: round1(weight * 100), Summary: summary,
	}
}

// combine folds component evidence into an overall verdict. Any unknown
// component forces the line itself to unknown with no numeric score.
func combine(components []Evidence) (Status, *float64) {
	weighted, weightSum := 0.0, 0.0
	for _, component := range components {
		weightSum += component.Weight
		if component.State == StateUnknown || component.Score == nil {
			return StatusUnknown, nil
		}
		weighted += component.Weight * *component.Score
	}
	if weightSum <= 0 {
		return StatusUnknown, nil
	}
	score := round1(weighted / weightSum)
	return statusForScore(score), &score
}

// dominantEvidence returns the evidence contributing most to the verdict:
// the highest weighted loss, ties broken by component order.
func dominantEvidence(components []Evidence) *Evidence {
	var dominant *Evidence
	for index := range components {
		candidate := &components[index]
		if dominant == nil || candidate.Loss > dominant.Loss {
			dominant = candidate
		}
	}
	return dominant
}

// trendFor records a known score and compares it against the mean of the
// recent known scores for the route.
func (s *Service) trendFor(routeID string, score *float64, now time.Time) Trend {
	if score == nil {
		return TrendUnknown
	}
	s.mu.Lock()
	history := s.history[routeID]
	baseline, ok := baselineOf(history, s.config.TrendWindow)
	history = append(history, knownScore{score: *score, at: now})
	if len(history) > maxKnownScores {
		history = history[len(history)-maxKnownScores:]
	}
	s.history[routeID] = history
	s.mu.Unlock()
	if !ok {
		return TrendStable
	}
	delta := *score - baseline
	switch {
	case delta <= -s.config.TrendDelta:
		return TrendDegrading
	case delta >= s.config.TrendDelta:
		return TrendImproving
	default:
		return TrendStable
	}
}

func baselineOf(history []knownScore, window int) (float64, bool) {
	if len(history) == 0 {
		return 0, false
	}
	start := 0
	if len(history) > window {
		start = len(history) - window
	}
	sum := 0.0
	for _, entry := range history[start:] {
		sum += entry.score
	}
	return sum / float64(len(history)-start), true
}
