package linescore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/route"
	"github.com/wyw14/cry-103/internal/telemetry"
	"github.com/wyw14/cry-103/internal/topology"
)

type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time {
	return c.now
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)}
}

func newTestService(t *testing.T, clock *testClock) (*Service, *telemetry.Repository, *route.Route) {
	t.Helper()
	target, err := route.New("R-S3", "S3", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	repository := telemetry.NewRepository()
	evaluator, err := telemetry.NewAgeEvaluator(clock.Now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(map[string]*route.Route{"R-S3": target}, repository, evaluator, clock.Now, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	service.Register("R-S3", "R07")
	return service, repository, target
}

func addSample(t *testing.T, repository *telemetry.Repository, asset string, kind telemetry.Kind, value float64, sequence uint64, observedAt time.Time) {
	t.Helper()
	sample := telemetry.Sample{
		ID: fmt.Sprintf("sample-%s-%d", kind, sequence), AssetID: asset, Kind: kind,
		Value: value, Sequence: sequence, Generation: 1, ObservedAt: observedAt,
	}
	if err := repository.Add(sample); err != nil {
		t.Fatal(err)
	}
}

func seedHealthy(t *testing.T, repository *telemetry.Repository, at time.Time) {
	t.Helper()
	addSample(t, repository, "R07", telemetry.KindTemperature, 40, 1, at)
	addSample(t, repository, "R07", telemetry.KindOpticalPower, -5, 2, at)
	addSample(t, repository, "R07", telemetry.KindErrorRate, 1e-12, 3, at)
	addSample(t, repository, "R07", telemetry.KindHeartbeat, 1, 4, at)
}

func componentOf(t *testing.T, assessment Assessment, component Component) Evidence {
	t.Helper()
	for _, evidence := range assessment.Components {
		if evidence.Component == component {
			return evidence
		}
	}
	t.Fatalf("component %s missing from assessment", component)
	return Evidence{}
}

func TestAssessUnknownWhenSamplesMissing(t *testing.T) {
	clock := newTestClock()
	service, _, _ := newTestService(t, clock)

	assessment, err := service.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Status != StatusUnknown {
		t.Fatalf("status=%s, want unknown", assessment.Status)
	}
	if assessment.Score != nil {
		t.Fatalf("score=%v, want nil when sampling is missing", *assessment.Score)
	}
	if assessment.Trend != TrendUnknown {
		t.Fatalf("trend=%s, want unknown", assessment.Trend)
	}
	if assessment.Dominant == nil || assessment.Dominant.Component != ComponentTemperature {
		t.Fatalf("dominant=%+v, want temperature as the heaviest missing evidence", assessment.Dominant)
	}
	if assessment.Dominant.State != StateUnknown {
		t.Fatalf("dominant state=%s, want unknown", assessment.Dominant.State)
	}
	for _, evidence := range assessment.Components {
		if evidence.Component == ComponentProtection {
			if evidence.State != StateOK {
				t.Fatalf("protection state=%s, want ok on primary path", evidence.State)
			}
			continue
		}
		if evidence.State != StateUnknown || evidence.Score != nil {
			t.Fatalf("component %s should be unknown without score, got %+v", evidence.Component, evidence)
		}
	}
}

func TestAssessHealthyWithFullEvidence(t *testing.T) {
	clock := newTestClock()
	service, repository, _ := newTestService(t, clock)
	seedHealthy(t, repository, clock.Now())

	assessment, err := service.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Status != StatusHealthy {
		t.Fatalf("status=%s, want healthy", assessment.Status)
	}
	if assessment.Score == nil || *assessment.Score != 100 {
		t.Fatalf("score=%v, want 100", assessment.Score)
	}
	if assessment.Trend != TrendStable {
		t.Fatalf("trend=%s, want stable on first known assessment", assessment.Trend)
	}
	if assessment.SpanID != "S3" {
		t.Fatalf("span=%s, want S3", assessment.SpanID)
	}
}

func TestAssessDegradedOnProtectionPath(t *testing.T) {
	clock := newTestClock()
	service, repository, target := newTestService(t, clock)
	seedHealthy(t, repository, clock.Now())

	registry := topology.NewRegistry()
	if _, err := registry.Register("S3", topology.PathPrimary, clock.Now()); err != nil {
		t.Fatal(err)
	}
	activator := route.NewActivator(registry)
	if _, err := activator.Protection(context.Background(), target, clock.Now()); err != nil {
		t.Fatal(err)
	}

	assessment, err := service.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	protection := componentOf(t, assessment, ComponentProtection)
	if protection.State != StateDegraded {
		t.Fatalf("protection state=%s, want degraded", protection.State)
	}
	if assessment.Dominant == nil || assessment.Dominant.Component != ComponentProtection {
		t.Fatalf("dominant=%+v, want protection path evidence", assessment.Dominant)
	}
	if assessment.Score == nil || *assessment.Score >= 100 {
		t.Fatalf("score=%v, want a deduction for the protection path", assessment.Score)
	}
}

func TestAssessCriticalTemperatureDominates(t *testing.T) {
	clock := newTestClock()
	service, repository, _ := newTestService(t, clock)
	seedHealthy(t, repository, clock.Now())
	addSample(t, repository, "R07", telemetry.KindTemperature, 92, 5, clock.Now())

	assessment, err := service.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	temperature := componentOf(t, assessment, ComponentTemperature)
	if temperature.State != StateCritical {
		t.Fatalf("temperature state=%s, want critical", temperature.State)
	}
	if temperature.AssetID != "R07" || temperature.ObservedAt == nil {
		t.Fatalf("temperature evidence should name the asset and sample time: %+v", temperature)
	}
	if assessment.Dominant == nil || assessment.Dominant.Component != ComponentTemperature {
		t.Fatalf("dominant=%+v, want temperature", assessment.Dominant)
	}
	if assessment.Status != StatusDegraded {
		t.Fatalf("status=%s, want degraded (single tripped repeater)", assessment.Status)
	}
}

func TestTrendDegradingThenImproving(t *testing.T) {
	clock := newTestClock()
	service, repository, _ := newTestService(t, clock)
	seedHealthy(t, repository, clock.Now())

	first, err := service.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if first.Trend != TrendStable {
		t.Fatalf("first trend=%s, want stable", first.Trend)
	}

	addSample(t, repository, "R07", telemetry.KindOpticalPower, -35, 6, clock.Now())
	second, err := service.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if second.Trend != TrendDegrading {
		t.Fatalf("trend=%s, want degrading after optical power collapse", second.Trend)
	}
	if componentOf(t, second, ComponentOpticalPower).State != StateCritical {
		t.Fatalf("optical state should be critical at -35 dBm")
	}

	addSample(t, repository, "R07", telemetry.KindOpticalPower, -4, 7, clock.Now())
	third, err := service.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if third.Trend != TrendImproving {
		t.Fatalf("trend=%s, want improving after optical power recovers", third.Trend)
	}
}

func TestStaleSamplesAreUnknown(t *testing.T) {
	clock := newTestClock()
	service, repository, _ := newTestService(t, clock)
	stale := clock.Now().Add(-time.Minute)
	addSample(t, repository, "R07", telemetry.KindTemperature, 40, 1, stale)
	addSample(t, repository, "R07", telemetry.KindOpticalPower, -5, 2, stale)
	addSample(t, repository, "R07", telemetry.KindErrorRate, 1e-12, 3, stale)
	addSample(t, repository, "R07", telemetry.KindHeartbeat, 1, 4, stale)

	assessment, err := service.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Status != StatusUnknown {
		t.Fatalf("status=%s, want unknown when every sample is stale", assessment.Status)
	}
	if assessment.Score != nil {
		t.Fatalf("score=%v, want nil for stale sampling", *assessment.Score)
	}
}

func TestHeartbeatAgeScoring(t *testing.T) {
	clock := newTestClock()
	service, repository, _ := newTestService(t, clock)
	seedHealthy(t, repository, clock.Now())
	addSample(t, repository, "R07", telemetry.KindHeartbeat, 1, 5, clock.Now().Add(-20*time.Second))

	assessment, err := service.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := componentOf(t, assessment, ComponentHeartbeatAge)
	if heartbeat.State != StateDegraded {
		t.Fatalf("heartbeat state=%s, want degraded at 20s age against 10s timeout", heartbeat.State)
	}
	if heartbeat.Score == nil || *heartbeat.Score != 50 {
		t.Fatalf("heartbeat score=%v, want 50", heartbeat.Score)
	}
}

func TestAssessSpanGroupsRoutes(t *testing.T) {
	clock := newTestClock()
	service, repository, _ := newTestService(t, clock)
	other, err := route.New("R-S9", "S9", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.routes["R-S9"] = other
	service.mu.Unlock()
	seedHealthy(t, repository, clock.Now())

	assessments, err := service.AssessSpan("S3")
	if err != nil {
		t.Fatal(err)
	}
	if len(assessments) != 1 || assessments[0].RouteID != "R-S3" {
		t.Fatalf("assessments=%+v, want only R-S3", assessments)
	}
	if _, err := service.Assess("R-S9"); err != nil {
		t.Fatal(err)
	}
}

func TestAssessUnknownRoute(t *testing.T) {
	clock := newTestClock()
	service, _, _ := newTestService(t, clock)
	if _, err := service.Assess("R-missing"); err == nil {
		t.Fatal("expected an error for an unknown route")
	}
}

func TestConfigValidation(t *testing.T) {
	clock := newTestClock()
	target, err := route.New("R-S3", "S3", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	repository := telemetry.NewRepository()
	evaluator, err := telemetry.NewAgeEvaluator(clock.Now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string]*route.Route{"R-S3": target}

	invalid := DefaultConfig()
	invalid.Thresholds.TemperatureWarnC = 90
	if _, err := NewService(routes, repository, evaluator, clock.Now, invalid); err == nil {
		t.Fatal("expected warn >= trip to be rejected")
	}

	invalid = DefaultConfig()
	invalid.Weights = Weights{}
	if _, err := NewService(routes, repository, evaluator, clock.Now, invalid); err == nil {
		t.Fatal("expected zero weights to be rejected")
	}

	unnormalized := DefaultConfig()
	unnormalized.Weights = Weights{Temperature: 3, OpticalPower: 2.5, ErrorRate: 2.5, HeartbeatAge: 1, Protection: 1}
	service, err := NewService(routes, repository, evaluator, clock.Now, unnormalized)
	if err != nil {
		t.Fatal(err)
	}
	service.Register("R-S3", "R07")
	seedHealthy(t, repository, clock.Now())
	assessment, err := service.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Score == nil || *assessment.Score != 100 {
		t.Fatalf("score=%v, want 100 with normalized weights", assessment.Score)
	}
}
