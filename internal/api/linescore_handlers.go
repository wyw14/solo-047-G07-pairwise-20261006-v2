package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// getRouteLineHealth serves the routing page: line health for one route.
func (s *Server) getRouteLineHealth(writer http.ResponseWriter, request *http.Request) {
	routeID := chi.URLParam(request, "id")
	if _, err := s.services.Route(routeID); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	assessment, err := s.services.LineHealth.Assess(routeID)
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writeJSON(writer, http.StatusOK, assessment)
}

// getSpanLineHealth serves the maintenance page: line health for every route
// carrying the span.
func (s *Server) getSpanLineHealth(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	if _, err := s.services.Span(spanID); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	assessments, err := s.services.LineHealth.AssessSpan(spanID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, LineHealthListResponse{Items: assessments})
}
