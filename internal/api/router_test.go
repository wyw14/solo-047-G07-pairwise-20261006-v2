package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/wyw14/cry-103/internal/feed"
	"github.com/wyw14/cry-103/internal/healthscore"
	"github.com/wyw14/cry-103/internal/incident"
	"github.com/wyw14/cry-103/internal/landing"
	"github.com/wyw14/cry-103/internal/otdr"
	"github.com/wyw14/cry-103/internal/platform"
	"github.com/wyw14/cry-103/internal/repair"
	"github.com/wyw14/cry-103/internal/repeater"
	"github.com/wyw14/cry-103/internal/route"
	"github.com/wyw14/cry-103/internal/span"
	"github.com/wyw14/cry-103/internal/store"
	"github.com/wyw14/cry-103/internal/switchover"
	"github.com/wyw14/cry-103/internal/telemetry"
	"github.com/wyw14/cry-103/internal/topology"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	clock := fixedClock{now: time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC)}
	ids := platform.UUIDGenerator{}
	registry := topology.NewRegistry()
	if _, err := registry.Register("S3", topology.PathPrimary, clock.Now()); err != nil {
		t.Fatal(err)
	}
	target, err := span.New("S3", "S3", 126, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	path, err := route.New("R-S3", "S3", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	drainer := route.NewDrainer(0)
	activator := route.NewActivator(registry)
	receipts := landing.NewReceiptRegistry()
	permitService := repair.NewPermitService(feed.NewIsolationService(), receipts, ids, clock.Now)
	circuit, err := feed.NewCircuit("B", "east", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	permitService.RegisterCircuit(circuit)
	repeaterAsset, err := repeater.New("R07", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := incident.NewRepeaterLifecycle()
	projector := repeater.NewHealthProjector(lifecycle, 85)
	projector.Register(repeaterAsset)
	repository := telemetry.NewRepository()
	routes := map[string]*route.Route{"R-S3": path}
	scorer, err := healthscore.NewScorer(repository,
		NewLineLookup(routes, map[string][]string{"R-S3": {"R07"}}),
		healthscore.DefaultThresholds(), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	acceptance := repair.NewAcceptanceService(switchover.NewRestorer(drainer, activator, registry, ids, clock.Now), clock.Now)
	acceptance.Register("S3", target, path)
	services := &Services{
		Clock: clock, IDs: ids, Journal: store.NewJournal(filepath.Join(t.TempDir(), "events.jsonl")),
		Spans: map[string]*span.Span{"S3": target}, Routes: routes,
		Repeaters: map[string]*repeater.Repeater{"R07": repeaterAsset}, Topology: registry,
		OTDR:        otdr.NewSessionService(registry, landing.NewScanner(registry, 0), ids, clock.Now),
		Switchovers: switchover.NewSessionService(drainer, activator, ids, clock.Now, time.Second),
		Permits:     permitService, Receipts: receipts, Acceptance: acceptance,
		Telemetry: telemetry.NewIngestor(repository, clock.Now, projector), TelemetryRepository: repository,
		Admission:   incident.NewAdmissionService(telemetry.NewMuteManager(clock.Now)),
		HealthScore: scorer,
	}
	return NewServer(services)
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

func TestHealthAndScanHTTPFlow(t *testing.T) {
	server := testServer(t)
	health := httptest.NewRecorder()
	server.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", health.Code, health.Body.String())
	}

	body, _ := json.Marshal(ScanRequest{StationID: "east", LengthKM: 126, Chunks: 3})
	scan := httptest.NewRecorder()
	server.Handler().ServeHTTP(scan, httptest.NewRequest(http.MethodPost, "/api/spans/S3/otdr-scans", bytes.NewReader(body)))
	if scan.Code != http.StatusCreated {
		t.Fatalf("scan status=%d body=%s", scan.Code, scan.Body.String())
	}
	var trace otdr.Trace
	if err := json.Unmarshal(scan.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.SpanID != "S3" || len(trace.Chunks) != 3 {
		t.Fatalf("unexpected trace: %+v", trace)
	}

	status := httptest.NewRecorder()
	server.Handler().ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", status.Code, status.Body.String())
	}
}

func TestLineHealthHTTPFlow(t *testing.T) {
	server := testServer(t)

	// No telemetry yet: the line must report unknown, not a health verdict.
	unknown := httptest.NewRecorder()
	server.Handler().ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/api/routes/R-S3/health", nil))
	if unknown.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	var assessment healthscore.Assessment
	if err := json.Unmarshal(unknown.Body.Bytes(), &assessment); err != nil {
		t.Fatal(err)
	}
	if assessment.Grade != healthscore.GradeUnknown || assessment.Score != nil {
		t.Fatalf("grade=%s score=%v, want unknown with nil score", assessment.Grade, assessment.Score)
	}
	if len(assessment.Missing) == 0 {
		t.Fatal("missing components should be listed when samples are absent")
	}

	// Feed one sample of each kind, then expect a graded assessment.
	samples := []TelemetryRequest{
		{Kind: "temperature", Value: 41, Unit: "C", Sequence: 1, Generation: 1},
		{Kind: "optical_power", Value: -4, Unit: "dBm", Sequence: 2, Generation: 1},
		{Kind: "error_rate", Value: 1e-10, Unit: "BER", Sequence: 3, Generation: 1},
		{Kind: "heartbeat", Value: 1, Unit: "beat", Sequence: 4, Generation: 1},
	}
	for _, sample := range samples {
		body, _ := json.Marshal(sample)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/repeaters/R07/telemetry", bytes.NewReader(body)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("ingest status=%d body=%s", response.Code, response.Body.String())
		}
	}

	graded := httptest.NewRecorder()
	server.Handler().ServeHTTP(graded, httptest.NewRequest(http.MethodGet, "/api/routes/R-S3/health", nil))
	if graded.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", graded.Code, graded.Body.String())
	}
	if err := json.Unmarshal(graded.Body.Bytes(), &assessment); err != nil {
		t.Fatal(err)
	}
	if assessment.Grade != healthscore.GradeHealthy {
		t.Fatalf("grade=%s want healthy, assessment=%+v", assessment.Grade, assessment)
	}
	if len(assessment.Components) != 5 {
		t.Fatalf("components=%d want 5", len(assessment.Components))
	}

	// The repair page queries line health by span ID.
	repair := httptest.NewRecorder()
	server.Handler().ServeHTTP(repair, httptest.NewRequest(http.MethodGet, "/api/repairs/S3/line-health", nil))
	if repair.Code != http.StatusOK {
		t.Fatalf("repair health status=%d body=%s", repair.Code, repair.Body.String())
	}
	var repairAssessment healthscore.Assessment
	if err := json.Unmarshal(repair.Body.Bytes(), &repairAssessment); err != nil {
		t.Fatal(err)
	}
	if repairAssessment.RouteID != "R-S3" || repairAssessment.SpanID != "S3" {
		t.Fatalf("repair assessment targets route=%s span=%s", repairAssessment.RouteID, repairAssessment.SpanID)
	}

	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/routes/R-unknown/health", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown route status=%d want 404", missing.Code)
	}
}
