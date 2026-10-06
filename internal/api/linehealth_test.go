package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wyw14/cry-103/internal/linescore"
)

func postTelemetry(t *testing.T, server *Server, repeaterID string, input TelemetryRequest) {
	t.Helper()
	body, _ := json.Marshal(input)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/repeaters/"+repeaterID+"/telemetry", bytes.NewReader(body)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("telemetry status=%d body=%s", response.Code, response.Body.String())
	}
}

func getRouteLineHealth(t *testing.T, server *Server, routeID string) linescore.Assessment {
	t.Helper()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/routes/"+routeID+"/line-health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("line health status=%d body=%s", response.Code, response.Body.String())
	}
	var assessment linescore.Assessment
	if err := json.Unmarshal(response.Body.Bytes(), &assessment); err != nil {
		t.Fatal(err)
	}
	return assessment
}

func TestRouteLineHealthUnknownWithoutSamples(t *testing.T) {
	server := testServer(t)
	assessment := getRouteLineHealth(t, server, "R-S3")
	if assessment.Status != linescore.StatusUnknown {
		t.Fatalf("status=%s, want unknown when no telemetry has been sampled", assessment.Status)
	}
	if assessment.Score != nil {
		t.Fatalf("score=%v, want null score instead of a health conclusion", *assessment.Score)
	}
	if assessment.Trend != linescore.TrendUnknown {
		t.Fatalf("trend=%s, want unknown", assessment.Trend)
	}
	if assessment.Dominant == nil || assessment.Dominant.Component != linescore.ComponentTemperature {
		t.Fatalf("dominant=%+v, want the heaviest missing evidence (temperature)", assessment.Dominant)
	}
}

func TestRouteLineHealthFlow(t *testing.T) {
	server := testServer(t)
	postTelemetry(t, server, "R07", TelemetryRequest{Kind: "temperature", Value: 40, Sequence: 1, Generation: 1})
	postTelemetry(t, server, "R07", TelemetryRequest{Kind: "optical_power", Value: -5, Sequence: 2, Generation: 1})
	postTelemetry(t, server, "R07", TelemetryRequest{Kind: "error_rate", Value: 1e-12, Sequence: 3, Generation: 1})
	postTelemetry(t, server, "R07", TelemetryRequest{Kind: "heartbeat", Value: 1, Sequence: 4, Generation: 1})

	assessment := getRouteLineHealth(t, server, "R-S3")
	if assessment.Status != linescore.StatusHealthy {
		t.Fatalf("status=%s, want healthy", assessment.Status)
	}
	if assessment.Score == nil || *assessment.Score != 100 {
		t.Fatalf("score=%v, want 100", assessment.Score)
	}
	if assessment.Trend != linescore.TrendStable {
		t.Fatalf("trend=%s, want stable", assessment.Trend)
	}

	postTelemetry(t, server, "R07", TelemetryRequest{Kind: "temperature", Value: 92, Sequence: 5, Generation: 1})
	assessment = getRouteLineHealth(t, server, "R-S3")
	if assessment.Status != linescore.StatusDegraded {
		t.Fatalf("status=%s, want degraded after temperature trip", assessment.Status)
	}
	if assessment.Dominant == nil || assessment.Dominant.Component != linescore.ComponentTemperature {
		t.Fatalf("dominant=%+v, want temperature", assessment.Dominant)
	}
	if assessment.Dominant.AssetID != "R07" {
		t.Fatalf("dominant asset=%s, want R07", assessment.Dominant.AssetID)
	}
	if assessment.Trend != linescore.TrendDegrading {
		t.Fatalf("trend=%s, want degrading", assessment.Trend)
	}
}

func TestSpanLineHealth(t *testing.T) {
	server := testServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/spans/S3/line-health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("span line health status=%d body=%s", response.Code, response.Body.String())
	}
	var list LineHealthListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].RouteID != "R-S3" {
		t.Fatalf("items=%+v, want the R-S3 assessment", list.Items)
	}

	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/spans/unknown/line-health", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing span status=%d, want 404", missing.Code)
	}
}

func TestRouteLineHealthNotFound(t *testing.T) {
	server := testServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/routes/R-missing/line-health", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing route status=%d, want 404", response.Code)
	}
}
