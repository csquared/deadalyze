package main

import (
	"math"
	"sort"

	"github.com/csquared/deadcatalog/analysis"
)

// Thresholds: a grid passes when its tempo, its bar phase and its bar
// count all land on the reference. Phase is the signal that matters on
// stage: a one-beat offset reads as 1.0 here.
const (
	maxBPMDelta   = 0.5  // BPM
	maxPhaseBeats = 0.25 // |downbeat phase offset| in beats
)

// Reference is a hand-reviewed grid for one file: what the engine must
// keep producing.
type Reference struct {
	// Path is relative to the corpus directory.
	Path            string  `json:"path"`
	BPM             float64 `json:"bpm"`
	FirstDownbeatMs int     `json:"first_downbeat_ms"`
	BeatsPerBar     int     `json:"beats_per_bar"`
	Note            string  `json:"note,omitempty"`
}

// Score is one file's grid against its reference.
type Score struct {
	Path            string  `json:"path"`
	BPM             float64 `json:"bpm"`
	BPMRef          float64 `json:"bpm_ref"`
	BPMDelta        float64 `json:"bpm_delta"`
	FirstDownbeatMs int     `json:"first_downbeat_ms"`
	// PhaseBeats is the signed downbeat offset in beats (ours − reference)
	// folded into a bar, centred: a one-beat slip reads ±1, half a bar ±2.
	PhaseBeats     float64 `json:"phase_beats"`
	BeatsPerBar    int     `json:"beats_per_bar"`
	BeatsPerBarRef int     `json:"beats_per_bar_ref"`
	// Bars is the whole-bar part of the same offset, which the fold hides:
	// a grid that starts a bar late is wrong even when its phase is 0.
	Bars     int     `json:"bars"`
	Dispute  string  `json:"dispute,omitempty"`
	Error    string  `json:"error,omitempty"`
	Pass     bool    `json:"pass"`
	Duration float64 `json:"duration"`
}

// score compares a fresh result with its reference.
func score(ref Reference, r *analysis.Result) Score {
	s := Score{Path: ref.Path, BPMRef: ref.BPM}
	if r == nil {
		s.Error = "no result"
		return s
	}
	s.BPM = r.BPM
	s.BPMDelta = r.BPM - ref.BPM
	s.Dispute = r.Dispute
	first, ok := firstDownbeat(r)
	if !ok {
		s.Error = "grid has no downbeat"
		return s
	}
	s.FirstDownbeatMs = first
	bar := ref.BeatsPerBar
	if bar <= 0 {
		bar = 4
	}
	s.BeatsPerBar, s.BeatsPerBarRef = r.Grid.BeatsPerBar, bar
	s.PhaseBeats, s.Bars = phase(float64(first-ref.FirstDownbeatMs)/1000, period(ref, r), bar)
	s.Pass = s.BeatsPerBar == bar && math.Abs(s.BPMDelta) <= maxBPMDelta && math.Abs(s.PhaseBeats) <= maxPhaseBeats && s.Bars == 0
	return s
}

// firstDownbeat is the result's first downbeat: the grid's own field, else
// the first beat numbered 1, else the first listed downbeat.
func firstDownbeat(r *analysis.Result) (int, bool) {
	if r.Grid.FirstDownbeatMs != nil {
		return *r.Grid.FirstDownbeatMs, true
	}
	for _, b := range r.Beats {
		if b.BeatNumber == 1 {
			return b.TimeMs, true
		}
	}
	if len(r.Downbeats) > 0 {
		return r.Downbeats[0].TimeMs, true
	}
	return 0, false
}

// period is the reference's beat length in seconds, the ruler the offset
// is measured with; the result's median beat spacing when the reference
// has no tempo.
func period(ref Reference, r *analysis.Result) float64 {
	if ref.BPM > 0 {
		return 60 / ref.BPM
	}
	if len(r.Beats) >= 2 {
		diffs := make([]float64, 0, len(r.Beats)-1)
		for i := 1; i < len(r.Beats); i++ {
			if d := r.Beats[i].TimeMs - r.Beats[i-1].TimeMs; d > 0 {
				diffs = append(diffs, float64(d)/1000)
			}
		}
		if len(diffs) > 0 {
			sort.Float64s(diffs)
			return diffs[len(diffs)/2]
		}
	}
	if r.BPM > 0 {
		return 60 / r.BPM
	}
	return 0
}

// phase splits a downbeat offset (seconds) into its within-bar part in
// beats, folded to (−bar/2, bar/2], and the whole bars left over. A grid
// exactly one bar late is (0, 1); one beat early is (−1, 0).
func phase(offsetSeconds, periodSeconds float64, beatsPerBar int) (beats float64, bars int) {
	if periodSeconds <= 0 || beatsPerBar <= 0 {
		return 0, 0
	}
	total := offsetSeconds / periodSeconds
	bar := float64(beatsPerBar)
	beats = math.Mod(total, bar)
	if beats > bar/2 {
		beats -= bar
	}
	if beats <= -bar/2 {
		beats += bar
	}
	return beats, int(math.Round((total - beats) / bar))
}
