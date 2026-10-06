package healthscore

import (
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/route"
	"github.com/wyw14/cry-103/internal/telemetry"
)

type stubLookup struct {
	snapshot route.Snapshot
	assets   []string
	err      error
}

func (l stubLookup) RouteSnapshot(string) (route.Snapshot, error) {
	return l.snapshot, l.err
}

func (l stubLookup) Assets(string) []string {
	return l.assets
}

func testClock() func() time.Time {
	return func() time.Time {
		return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	}
}

func addSample(t *testing.T, repository *telemetry.Repository, assetID string, kind telemetry.Kind, value float64, sequence uint64) {
	t.Helper()
	if err := repository.Add(telemetry.Sample{
		ID: assetID + "-" + string(kind), AssetID: assetID, Kind: kind, Value: value,
		Sequence: sequence, ObservedAt: testClock()(),
	}); err != nil {
		t.Fatal(err)
	}
}

func healthyLookup() stubLookup {
	return stubLookup{
		snapshot: route.Snapshot{ID: "R-S3", SpanID: "S3", State: route.StatePrimary},
		assets:   []string{"R07"},
	}
}

func seededRepository(t *testing.T) *telemetry.Repository {
	t.Helper()
	repository := telemetry.NewRepository()
	addSample(t, repository, "R07", telemetry.KindTemperature, 40, 1)
	addSample(t, repository, "R07", telemetry.KindOpticalPower, -3, 2)
	addSample(t, repository, "R07", telemetry.KindErrorRate, 1e-10, 3)
	addSample(t, repository, "R07", telemetry.KindHeartbeat, 1, 4)
	return repository
}

func TestAssessHealthyLine(t *testing.T) {
	scorer, err := NewScorer(seededRepository(t), healthyLookup(), DefaultThresholds(), testClock())
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := scorer.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Grade != GradeHealthy {
		t.Fatalf("grade=%s want healthy, assessment=%+v", assessment.Grade, assessment)
	}
	if assessment.Score == nil || *assessment.Score < gradeHealthyFloor {
		t.Fatalf("score=%v want >= %v", assessment.Score, gradeHealthyFloor)
	}
	if assessment.Trend != TrendUnknown {
		t.Fatalf("first assessment trend=%s want unknown", assessment.Trend)
	}
	if assessment.Dominant != nil {
		t.Fatalf("healthy line should have no dominant deduction, got %+v", assessment.Dominant)
	}
	if len(assessment.Components) != 5 {
		t.Fatalf("components=%d want 5", len(assessment.Components))
	}
}

func TestAssessUnknownWhenSamplesMissing(t *testing.T) {
	repository := telemetry.NewRepository()
	addSample(t, repository, "R07", telemetry.KindTemperature, 40, 1)
	scorer, err := NewScorer(repository, healthyLookup(), DefaultThresholds(), testClock())
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := scorer.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Grade != GradeUnknown {
		t.Fatalf("grade=%s want unknown when samples are missing", assessment.Grade)
	}
	if assessment.Score != nil {
		t.Fatalf("score should be nil for unknown grade, got %v", *assessment.Score)
	}
	if assessment.Trend != TrendUnknown {
		t.Fatalf("trend=%s want unknown", assessment.Trend)
	}
	wantMissing := map[Component]bool{
		ComponentOpticalPower: true, ComponentErrorRate: true, ComponentHeartbeatAge: true,
	}
	if len(assessment.Missing) != len(wantMissing) {
		t.Fatalf("missing=%v want %d entries", assessment.Missing, len(wantMissing))
	}
	for _, component := range assessment.Missing {
		if !wantMissing[component] {
			t.Fatalf("unexpected missing component %s", component)
		}
		delete(wantMissing, component)
	}
	for _, component := range assessment.Components {
		if component.Component == ComponentTemperature && component.State != StateOK {
			t.Fatalf("temperature state=%s want ok", component.State)
		}
	}
}

func TestAssessDominantEvidence(t *testing.T) {
	repository := seededRepository(t)
	addSample(t, repository, "R07", telemetry.KindTemperature, 82, 5)
	scorer, err := NewScorer(repository, healthyLookup(), DefaultThresholds(), testClock())
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := scorer.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Grade == GradeHealthy || assessment.Grade == GradeUnknown {
		t.Fatalf("grade=%s want degraded or critical", assessment.Grade)
	}
	if assessment.Dominant == nil {
		t.Fatal("dominant evidence is required for a degraded line")
	}
	if assessment.Dominant.Component != ComponentTemperature {
		t.Fatalf("dominant=%s want temperature", assessment.Dominant.Component)
	}
	if assessment.Dominant.AssetID != "R07" {
		t.Fatalf("dominant asset=%s want R07", assessment.Dominant.AssetID)
	}
	if assessment.Dominant.Value == nil || *assessment.Dominant.Value != 82 {
		t.Fatalf("dominant value=%v want 82", assessment.Dominant.Value)
	}
}

func TestAssessProtectionPathLowersScore(t *testing.T) {
	lookup := healthyLookup()
	lookup.snapshot.State = route.StateProtectionActive
	scorer, err := NewScorer(seededRepository(t), lookup, DefaultThresholds(), testClock())
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := scorer.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	var protection *ComponentScore
	for index := range assessment.Components {
		if assessment.Components[index].Component == ComponentProtection {
			protection = &assessment.Components[index]
		}
	}
	if protection == nil || protection.Score == nil {
		t.Fatalf("protection component missing: %+v", assessment.Components)
	}
	if *protection.Score != 30 {
		t.Fatalf("protection score=%v want 30", *protection.Score)
	}
	if assessment.Dominant == nil || assessment.Dominant.Component != ComponentProtection {
		t.Fatalf("dominant=%+v want protection_path", assessment.Dominant)
	}
}

func TestAssessTrendDegrades(t *testing.T) {
	repository := seededRepository(t)
	scorer, err := NewScorer(repository, healthyLookup(), DefaultThresholds(), testClock())
	if err != nil {
		t.Fatal(err)
	}
	first, err := scorer.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if first.Trend != TrendUnknown {
		t.Fatalf("first trend=%s want unknown", first.Trend)
	}
	addSample(t, repository, "R07", telemetry.KindOpticalPower, -27, 5)
	second, err := scorer.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if second.Trend != TrendDegrading {
		t.Fatalf("trend=%s want degrading (score %v -> %v)", second.Trend, *first.Score, *second.Score)
	}
	third, err := scorer.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if third.Trend != TrendStable {
		t.Fatalf("trend=%s want stable on repeated assessment", third.Trend)
	}
}

func TestAssessStaleHeartbeat(t *testing.T) {
	repository := seededRepository(t)
	stale := telemetry.Sample{
		ID: "R07-hb-stale", AssetID: "R07", Kind: telemetry.KindHeartbeat, Value: 1,
		Sequence: 9, ObservedAt: testClock()().Add(-time.Minute),
	}
	if err := repository.Add(stale); err != nil {
		t.Fatal(err)
	}
	scorer, err := NewScorer(repository, healthyLookup(), DefaultThresholds(), testClock())
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := scorer.Assess("R-S3")
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Dominant == nil || assessment.Dominant.Component != ComponentHeartbeatAge {
		t.Fatalf("dominant=%+v want heartbeat_age", assessment.Dominant)
	}
}

func TestAssessUnknownRoute(t *testing.T) {
	scorer, err := NewScorer(seededRepository(t), healthyLookup(), DefaultThresholds(), testClock())
	if err != nil {
		t.Fatal(err)
	}
	lookupError := stubLookup{err: errTest}
	scorer.lines = lookupError
	if _, err := scorer.Assess("missing"); err == nil {
		t.Fatal("expected error for unknown route")
	}
}

var errTest = errorString("route missing")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestThresholdsValidate(t *testing.T) {
	if err := DefaultThresholds().Validate(); err != nil {
		t.Fatalf("default thresholds invalid: %v", err)
	}
	invalid := DefaultThresholds()
	invalid.TemperatureLimitC = invalid.TemperatureNominalC
	if _, err := NewScorer(telemetry.NewRepository(), healthyLookup(), invalid, testClock()); err == nil {
		t.Fatal("expected validation error for inverted temperature thresholds")
	}
}
