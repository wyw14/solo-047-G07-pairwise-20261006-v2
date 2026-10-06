package healthscore

import "time"

// Grade is the line-level health verdict. GradeUnknown means sampling is
// incomplete and no health conclusion is drawn.
type Grade string

const (
	GradeHealthy  Grade = "healthy"
	GradeDegraded Grade = "degraded"
	GradeCritical Grade = "critical"
	GradeUnknown  Grade = "unknown"
)

// Trend describes how the line score moves between consecutive assessments.
type Trend string

const (
	TrendImproving Trend = "improving"
	TrendStable    Trend = "stable"
	TrendDegrading Trend = "degrading"
	TrendUnknown   Trend = "unknown"
)

// Component identifies one scored signal.
type Component string

const (
	ComponentTemperature  Component = "temperature"
	ComponentOpticalPower Component = "optical_power"
	ComponentErrorRate    Component = "error_rate"
	ComponentHeartbeatAge Component = "heartbeat_age"
	ComponentProtection   Component = "protection_path"
)

// ComponentState is the per-signal verdict.
type ComponentState string

const (
	StateOK       ComponentState = "ok"
	StateWarn     ComponentState = "warn"
	StateCritical ComponentState = "critical"
	StateUnknown  ComponentState = "unknown"
)

// ComponentScore is one signal's contribution to the line score. Score is
// nil when the underlying samples are missing.
type ComponentScore struct {
	Component  Component      `json:"component"`
	State      ComponentState `json:"state"`
	Score      *float64       `json:"score"`
	Weight     float64        `json:"weight"`
	Evidence   string         `json:"evidence"`
	AssetID    string         `json:"asset_id,omitempty"`
	Value      *float64       `json:"value,omitempty"`
	Unit       string         `json:"unit,omitempty"`
	ObservedAt *time.Time     `json:"observed_at,omitempty"`
}

// Assessment is the health verdict for one line at a point in time.
type Assessment struct {
	RouteID    string           `json:"route_id"`
	SpanID     string           `json:"span_id"`
	Grade      Grade            `json:"grade"`
	Score      *float64         `json:"score"`
	Trend      Trend            `json:"trend"`
	Components []ComponentScore `json:"components"`
	Dominant   *ComponentScore  `json:"dominant_evidence,omitempty"`
	Missing    []Component      `json:"missing,omitempty"`
	AssessedAt time.Time        `json:"assessed_at"`
}
