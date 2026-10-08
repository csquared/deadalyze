package main

import (
	"math"
	"testing"

	"github.com/csquared/deadcatalog/analysis"
)

func TestPhaseFoldsWithinTheBarAndCountsWholeBars(t *testing.T) {
	period := 0.5 // 120 BPM
	cases := []struct {
		offset float64
		beats  float64
		bars   int
	}{
		{0, 0, 0},
		{0.5, 1, 0},   // one beat late
		{-0.5, -1, 0}, // one beat early
		{1.0, 2, 0},   // half a bar: the far side
		{2.0, 0, 1},   // a whole bar late: phase 0, one bar
		{-2.0, 0, -1}, // a whole bar early
		{2.5, 1, 1},   // a bar and a beat
		{-2.5, -1, -1},
		{0.1, 0.2, 0}, // a fifth of a beat
	}
	for _, c := range cases {
		beats, bars := phase(c.offset, period, 4)
		if math.Abs(beats-c.beats) > 1e-9 || bars != c.bars {
			t.Errorf("phase(%v) = (%v, %d), want (%v, %d)", c.offset, beats, bars, c.beats, c.bars)
		}
	}
	if b, n := phase(1, 0, 4); b != 0 || n != 0 {
		t.Errorf("no period: got (%v, %d)", b, n)
	}
}

func result(bpm float64, firstDownbeatMs int, dispute string) *analysis.Result {
	first := firstDownbeatMs
	return &analysis.Result{BPM: bpm, Grid: analysis.Grid{BPM: bpm, FirstDownbeatMs: &first, BeatsPerBar: 4}, Dispute: dispute}
}

func TestScorePassesOnTheReferenceAndFailsOffIt(t *testing.T) {
	ref := Reference{Path: "a.mp3", BPM: 120, FirstDownbeatMs: 1000, BeatsPerBar: 4}
	if s := score(ref, result(120.2, 1050, "")); !s.Pass || s.Bars != 0 || math.Abs(s.PhaseBeats-0.1) > 1e-9 {
		t.Errorf("near the reference should pass: %+v", s)
	}
	if s := score(ref, result(121, 1000, "")); s.Pass || s.BPMDelta != 1 {
		t.Errorf("a BPM off by one should fail: %+v", s)
	}
	if s := score(ref, result(120, 1500, "")); s.Pass || math.Abs(s.PhaseBeats-1) > 1e-9 {
		t.Errorf("a beat late should fail: %+v", s)
	}
	// A whole bar late has phase 0: the bar count is what catches it.
	if s := score(ref, result(120, 3000, "")); s.Pass || s.Bars != 1 || s.PhaseBeats != 0 {
		t.Errorf("a bar late should fail on bars: %+v", s)
	}
	if s := score(ref, result(120, 1000, "phase: cross-checker votes beat 3")); !s.Pass || s.Dispute == "" {
		t.Errorf("a dispute is reported, not a failure by itself: %+v", s)
	}
	if s := score(ref, nil); s.Pass || s.Error == "" {
		t.Errorf("no result is an error: %+v", s)
	}
}

func TestFirstDownbeatFallsBackToBeatOne(t *testing.T) {
	r := &analysis.Result{Beats: []analysis.Beat{{TimeMs: 200, BeatNumber: 3}, {TimeMs: 700, BeatNumber: 4}, {TimeMs: 1200, BeatNumber: 1}}}
	if ms, ok := firstDownbeat(r); !ok || ms != 1200 {
		t.Errorf("got %d %v", ms, ok)
	}
	if _, ok := firstDownbeat(&analysis.Result{}); ok {
		t.Error("nothing to go on should not be a downbeat")
	}
}

func TestScoreRequiresMatchingMeter(t *testing.T) {
	for _, tt := range []struct {
		name             string
		meter, reference int
		pass             bool
	}{
		{"wrong meter", 3, 4, false},
		{"missing result meter", 0, 4, false},
		{"matching triple meter", 3, 3, true},
		{"reference defaults to four", 4, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := result(120, 1000, "")
			r.Grid.BeatsPerBar = tt.meter
			s := score(Reference{BPM: 120, FirstDownbeatMs: 1000, BeatsPerBar: tt.reference}, r)
			wantReference := tt.reference
			if wantReference <= 0 {
				wantReference = 4
			}
			if s.Pass != tt.pass || s.BeatsPerBar != tt.meter || s.BeatsPerBarRef != wantReference {
				t.Fatalf("unexpected meter score: %+v", s)
			}
		})
	}
}

func TestAReferenceBarOneBeforeZeroCountsNoBars(t *testing.T) {
	// rekordbox lists its first beat as beat 3 at 27 ms: its bar 1 is at
	// -949 ms, which no grid can start on. Ours starts on the next downbeat
	// and is in phase with it; the whole bar between them is not a fault.
	ref := Reference{Path: "a", BPM: 123, FirstDownbeatMs: -949, BeatsPerBar: 4}
	first := 980
	r := &analysis.Result{BPM: 123, Grid: analysis.Grid{BPM: 123, BeatsPerBar: 4, FirstDownbeatMs: &first}}
	s := score(ref, r)
	if !s.Pass || s.Bars != 0 || math.Abs(s.PhaseBeats) > 0.1 {
		t.Fatalf("score = %+v, want a pass in phase with no bars off", s)
	}
	// With an audible reference bar 1, a grid a bar late still fails.
	ref.FirstDownbeatMs = 1003
	late := 1003 + 1951
	r.Grid.FirstDownbeatMs = &late
	if s := score(ref, r); s.Pass || s.Bars != 1 {
		t.Fatalf("score = %+v, want bars +1", s)
	}
}
