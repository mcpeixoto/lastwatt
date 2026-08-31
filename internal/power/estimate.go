package power

import "time"

// Estimator smooths instantaneous draw into a usable runtime prediction.
//
// Two hardware quirks drive this design. First, many EC firmwares (the ASUS one
// this was written against included) report power_now as 0 while on mains, and
// it only becomes meaningful a few seconds after discharge actually begins --
// so zero samples must be discarded, not averaged in. Second, raw power_now is
// noisy enough that a naive reading swings the estimate by tens of minutes
// between samples, which would make percentage-independent tier triggers flap.
type Estimator struct {
	alpha  float64 // EWMA smoothing factor, 0<alpha<=1; lower is smoother
	ewma   float64 // watts
	count  int
	peakW  float64
	firstT time.Time
	lastT  time.Time
}

// NewEstimator returns an estimator. alpha of 0.2 over 1s samples gives a time
// constant of roughly 5 seconds, which tracks a real load change quickly while
// ignoring per-sample jitter.
func NewEstimator(alpha float64) *Estimator {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.2
	}
	return &Estimator{alpha: alpha}
}

// Add folds one sample in. Non-positive samples are ignored.
func (e *Estimator) Add(watts float64) {
	if watts <= 0 {
		return
	}
	now := time.Now()
	if e.count == 0 {
		e.ewma = watts
		e.firstT = now
	} else {
		e.ewma = e.alpha*watts + (1-e.alpha)*e.ewma
	}
	if watts > e.peakW {
		e.peakW = watts
	}
	e.lastT = now
	e.count++
}

// Reset clears state, e.g. when mains returns.
func (e *Estimator) Reset() { *e = Estimator{alpha: e.alpha} }

// Ready reports whether enough samples have accumulated to trust the estimate.
func (e *Estimator) Ready() bool { return e.count >= 3 }

// DrawW is the smoothed draw in watts, or 0 if not yet ready.
func (e *Estimator) DrawW() float64 {
	if !e.Ready() {
		return 0
	}
	return e.ewma
}

// PeakW is the highest sample seen since the last reset.
func (e *Estimator) PeakW() float64 { return e.peakW }

// Samples is the number of usable samples folded in.
func (e *Estimator) Samples() int { return e.count }

// Remaining predicts time left given remaining energy in watt-hours. It returns
// ok=false until the estimate is trustworthy, so callers can fall back to
// percentage-based triggers during the warm-up window rather than acting on a
// wild number.
func (e *Estimator) Remaining(energyWh float64) (time.Duration, bool) {
	w := e.DrawW()
	if w <= 0 || energyWh <= 0 {
		return 0, false
	}
	hours := energyWh / w
	return time.Duration(hours * float64(time.Hour)), true
}
