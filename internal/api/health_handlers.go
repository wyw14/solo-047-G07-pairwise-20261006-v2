package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/cry-103/internal/healthscore"
	"github.com/wyw14/cry-103/internal/route"
)

// lineLookup adapts the service route registry to healthscore.LineLookup.
type lineLookup struct {
	routes map[string]*route.Route
	assets map[string][]string
}

// NewLineLookup builds the healthscore.LineLookup over the shared route
// registry. lineAssets maps route ID to the telemetry asset IDs on that line.
func NewLineLookup(routes map[string]*route.Route, lineAssets map[string][]string) healthscore.LineLookup {
	return lineLookup{routes: routes, assets: lineAssets}
}

func (l lineLookup) RouteSnapshot(routeID string) (route.Snapshot, error) {
	target, exists := l.routes[routeID]
	if !exists {
		return route.Snapshot{}, &routeNotFoundError{routeID: routeID}
	}
	return target.Snapshot(), nil
}

func (l lineLookup) Assets(routeID string) []string {
	return append([]string(nil), l.assets[routeID]...)
}

type routeNotFoundError struct {
	routeID string
}

func (e *routeNotFoundError) Error() string {
	return "route " + e.routeID + " not found"
}

// getRouteHealth serves the routing page: health of one line by route ID.
func (s *Server) getRouteHealth(writer http.ResponseWriter, request *http.Request) {
	assessment, err := s.services.HealthScore.Assess(chi.URLParam(request, "id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writeJSON(writer, http.StatusOK, assessment)
}

// getRepairLineHealth serves the repair page: health of the line carried on
// the span under repair.
func (s *Server) getRepairLineHealth(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	if _, err := s.services.Span(spanID); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	for _, target := range s.services.Routes {
		if target.Snapshot().SpanID != spanID {
			continue
		}
		assessment, err := s.services.HealthScore.Assess(target.Snapshot().ID)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, assessment)
		return
	}
	writeJSON(writer, http.StatusNotFound, ErrorResponse{Error: "no route on span " + spanID})
}
