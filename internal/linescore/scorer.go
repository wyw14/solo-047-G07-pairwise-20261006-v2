package linescore

import (
	"errors"
	"math"
	"time"
)

// Weights apportion the overall score across components. Values are
// normalized to sum to one when the service is constructed.
type Weights struct {
	Temperature  float64
	OpticalPower float64
	ErrorRate    float64
	HeartbeatAge float64
	Protection   float64
}

func (w Weights) forComponent(component Component) float64 {
	switch component {
	case ComponentTemperature:
		return w.Temperature
	case ComponentOpticalPower:
		return w.OpticalPower
	case ComponentErrorRate:
		return w.ErrorRate
	case ComponentHeartbeatAge:
		return w.HeartbeatAge
	case ComponentProtection:
		return w.Protection
	}
	return 0
}

func (w Weights) total() float64 {
	return w.Temperature + w.OpticalPower + w.ErrorRate + w.HeartbeatAge + w.Protection
}

func (w Weights) normalized() Weights {
	total := w.total()
	if total <= 0 {
		return w
	}
	return Weights{
		Temperature:  w.Temperature / total,
		OpticalPower: w.OpticalPower / total,
		ErrorRate:    w.ErrorRate / total,
		HeartbeatAge: w.HeartbeatAge / total,
		Protection:   w.Protection / total,
	}
}

// Thresholds bound the ramps that map raw observations onto 0-100 scores.
type Thresholds struct {
	TemperatureWarnC    float64
	TemperatureTripC    float64
	OpticalGoodDBm      float64
	OpticalDarkDBm      float64
	ErrorRateGood       float64
	ErrorRateBad        float64
	HeartbeatTimeout    time.Duration
	ProtectionPathScore float64
}

// Config tunes scoring, sample freshness and trend detection.
type Config struct {
	Weights      Weights
	Thresholds   Thresholds
	SampleMaxAge time.Duration
	TrendDelta   float64
	TrendWindow  int
}

func DefaultConfig() Config {
	return Config{
		Weights: Weights{Temperature: 0.3, OpticalPower: 0.25, ErrorRate: 0.25, HeartbeatAge: 0.1, Protection: 0.1},
		Thresholds: Thresholds{
			TemperatureWarnC: 60, TemperatureTripC: 85,
			OpticalGoodDBm: -8, OpticalDarkDBm: -30,
			ErrorRateGood: 1e-11, ErrorRateBad: 1e-6,
			HeartbeatTimeout:    10 * time.Second,
			ProtectionPathScore: 50,
		},
		SampleMaxAge: 30 * time.Second,
		TrendDelta:   5,
		TrendWindow:  8,
	}
}

func (c Config) validate() error {
	if c.Weights.total() <= 0 {
		return errors.New("line score weights must sum to a positive value")
	}
	if !(c.Thresholds.TemperatureWarnC < c.Thresholds.TemperatureTripC) {
		return errors.New("temperature warn threshold must be below trip threshold")
	}
	if !(c.Thresholds.OpticalDarkDBm < c.Thresholds.OpticalGoodDBm) {
		return errors.New("optical dark threshold must be below good threshold")
	}
	if !(c.Thresholds.ErrorRateGood > 0 && c.Thresholds.ErrorRateGood < c.Thresholds.ErrorRateBad) {
		return errors.New("error-rate thresholds must be positive with good below bad")
	}
	if c.Thresholds.HeartbeatTimeout <= 0 {
		return errors.New("heartbeat timeout must be positive")
	}
	if c.Thresholds.ProtectionPathScore < 0 || c.Thresholds.ProtectionPathScore > 100 {
		return errors.New("protection path score must be within 0-100")
	}
	if c.SampleMaxAge <= 0 {
		return errors.New("sample max age must be positive")
	}
	if c.TrendDelta <= 0 || c.TrendWindow < 1 {
		return errors.New("trend delta and window must be positive")
	}
	return nil
}

// rampScore maps an ascending badness scale onto 100-0: full score at or
// below good, zero at or above bad, linear between.
func rampScore(value, good, bad float64) float64 {
	if value <= good {
		return 100
	}
	if value >= bad {
		return 0
	}
	return 100 * (bad - value) / (bad - good)
}

// rampScoreDescending is rampScore for metrics where lower is worse
// (good > bad), e.g. received optical power.
func rampScoreDescending(value, good, bad float64) float64 {
	if value >= good {
		return 100
	}
	if value <= bad {
		return 0
	}
	return 100 * (value - bad) / (good - bad)
}

// logRampScore interpolates on a log10 scale for quantities that span orders
// of magnitude, e.g. bit error rate.
func logRampScore(value, good, bad float64) float64 {
	if value <= 0 {
		return 100
	}
	low, high := math.Log10(good), math.Log10(bad)
	observed := math.Log10(value)
	if observed <= low {
		return 100
	}
	if observed >= high {
		return 0
	}
	return 100 * (high - observed) / (high - low)
}

func stateForScore(score float64) State {
	switch {
	case score >= 80:
		return StateOK
	case score >= 50:
		return StateDegraded
	default:
		return StateCritical
	}
}

func statusForScore(score float64) Status {
	switch {
	case score >= 80:
		return StatusHealthy
	case score >= 50:
		return StatusDegraded
	default:
		return StatusCritical
	}
}

func round1(value float64) float64 {
	return math.Round(value*10) / 10
}
