package linescore

import "time"

// Component identifies one line-health evidence input.
type Component string

const (
	ComponentTemperature  Component = "temperature"
	ComponentOpticalPower Component = "optical_power"
	ComponentErrorRate    Component = "error_rate"
	ComponentHeartbeatAge Component = "heartbeat_age"
	ComponentProtection   Component = "protection_path"
)

// componentOrder fixes evidence ordering and dominant-evidence tie-breaking.
var componentOrder = []Component{
	ComponentTemperature, ComponentOpticalPower, ComponentErrorRate, ComponentHeartbeatAge, ComponentProtection,
}

// State is the per-component condition derived from its score.
type State string

const (
	StateOK       State = "ok"
	StateDegraded State = "degraded"
	StateCritical State = "critical"
	StateUnknown  State = "unknown"
)

// Status is the overall line-health conclusion.
type Status string

const (
	StatusHealthy  Status = "healthy"
	StatusDegraded Status = "degraded"
	StatusCritical Status = "critical"
	StatusUnknown  Status = "unknown"
)

// Trend describes how the overall score is moving between assessments.
type Trend string

const (
	TrendImproving Trend = "improving"
	TrendStable    Trend = "stable"
	TrendDegrading Trend = "degrading"
	TrendUnknown   Trend = "unknown"
)

// Evidence is one component's contribution to a line assessment. Score is nil
// when the component has no usable samples: missing evidence must never be
// scored as healthy.
type Evidence struct {
	Component  Component  `json:"component"`
	State      State      `json:"state"`
	Score      *float64   `json:"score"`
	Weight     float64    `json:"weight"`
	Loss       float64    `json:"loss"`
	Summary    string     `json:"summary"`
	AssetID    string     `json:"asset_id,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// Assessment is the line-health verdict for one route. Score is nil whenever
// Status is StatusUnknown.
type Assessment struct {
	RouteID    string     `json:"route_id"`
	SpanID     string     `json:"span_id"`
	Status     Status     `json:"status"`
	Score      *float64   `json:"score"`
	Trend      Trend      `json:"trend"`
	Dominant   *Evidence  `json:"dominant_evidence,omitempty"`
	Components []Evidence `json:"components"`
	AssessedAt time.Time  `json:"assessed_at"`
}
