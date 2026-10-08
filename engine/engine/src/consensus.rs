//! The recipe: how two legs' grids become one answer. A port of
//! deadcatalog's `analysis/consensus.go` and `engine.go: arbitrate`, with
//! the thresholds read from the settings rather than compiled in. The
//! identity of this code is `RECIPE`.

use crate::settings::Settings;
use protocol::{BeatThisVote, Consensus, Verdict};

pub const RECIPE: &str = "deadca7-v2";

/// A leg's grid as the recipe sees it: times in ms on the leg's own
/// timeline, beat numbers 1..bar, downbeats where the number is 1.
#[derive(Debug, Clone, Default)]
pub struct LegGrid {
    pub bpm: f64,
    pub beats_per_bar: u32,
    /// (beat_number, time_ms)
    pub beats: Vec<(i64, i64)>,
    pub first_beat_ms: Option<i64>,
    pub first_downbeat_ms: Option<i64>,
    pub phase_vote: i64,
    pub phase_agreement: f64,
}

impl LegGrid {
    fn usable(&self) -> bool {
        !self.beats.is_empty() && self.bpm > 0.0
    }
    fn first_beat_s(&self) -> f64 {
        self.beats[0].1 as f64 / 1000.0
    }
    fn downbeats(&self) -> impl Iterator<Item = i64> + '_ {
        self.beats.iter().filter(|(n, _)| *n == 1).map(|(_, t)| *t)
    }
    fn first_downbeat_s(&self) -> Option<f64> {
        self.downbeats().next().map(|t| t as f64 / 1000.0)
    }
    fn bar(&self) -> i64 {
        if self.beats_per_bar > 0 {
            self.beats_per_bar as i64
        } else {
            4
        }
    }
    fn refresh_firsts(&mut self) {
        self.first_beat_ms = self.beats.first().map(|b| b.1);
        let first_downbeat = self.downbeats().next();
        self.first_downbeat_ms = first_downbeat;
    }
}

/// CompareGrids: "" when the two agree, else the semicolon-joined reasons.
pub fn compare(ours: &LegGrid, other: &LegGrid, s: &Settings) -> String {
    if !ours.usable() || !other.usable() {
        return "missing grid".into();
    }
    let mut reasons = Vec::new();
    if (ours.bpm - other.bpm).abs() > s.consensus_max_bpm_delta {
        reasons.push(format!("bpm {:.2} vs {:.2}", ours.bpm, other.bpm));
    }
    let period = 60.0 / ours.bpm;
    let mut origin = other.first_beat_s() - ours.first_beat_s();
    origin -= (origin / period).round() * period;
    if (origin * 1000.0).abs() > s.consensus_max_origin_ms {
        reasons.push(format!("origin {:+.0}ms", origin * 1000.0));
    }
    if let (Some(od), Some(td)) = (ours.first_downbeat_s(), other.first_downbeat_s()) {
        let bar = ours.bar() as f64;
        let mut phase = (td - od) / period;
        phase -= (phase / bar - 0.5).ceil() * bar;
        if phase.abs() > s.consensus_max_phase_beats {
            reasons.push(format!("downbeat {phase:+.1} beats"));
        }
    }
    reasons.join("; ")
}

/// HalfBeatShiftMS: the offbeat lock, and the shift that moves ours onto
/// other's lattice.
pub fn half_beat_shift_ms(ours: &LegGrid, other: &LegGrid, s: &Settings) -> Option<i64> {
    if !ours.usable() || !other.usable() {
        return None;
    }
    if (ours.bpm - other.bpm).abs() > s.consensus_max_bpm_delta {
        return None;
    }
    let period = 60.0 / ours.bpm;
    let mut d = other.first_beat_s() - ours.first_beat_s();
    d -= (d / period).round() * period;
    let ms = d * 1000.0;
    if ((ms.abs()) - period * 500.0).abs() > s.arbitrate_half_beat_slop_ms {
        return None;
    }
    Some(other.beats[0].1 - ours.beats[0].1)
}

/// PhaseRelabel: the relabel to adopt from a confident vote.
pub fn phase_relabel(other: &LegGrid, s: &Settings) -> Option<i64> {
    if other.phase_vote == 0 || other.phase_agreement < s.arbitrate_min_vote_agreement {
        return None;
    }
    Some(other.phase_vote)
}

/// PhaseDispute: the vote as an objection, folded past the half bar.
pub fn phase_dispute(other: &LegGrid, s: &Settings) -> Option<String> {
    if other.phase_vote == 0 || other.phase_agreement < s.consensus_min_vote_agreement {
        return None;
    }
    let mut v = other.phase_vote;
    let bar = other.bar();
    if v > bar / 2 {
        v -= bar;
    }
    Some(format!(
        "downbeat vote {:+} beats ({:.2} agree)",
        v, other.phase_agreement
    ))
}

/// ShiftGrid: every line by ms, clamped at 0, numbers kept.
pub fn shift(g: &mut LegGrid, ms: i64) {
    for b in &mut g.beats {
        b.1 = (b.1 + ms).max(0);
    }
    g.refresh_firsts();
}

/// RelabelGrid: the "1" moves `beats` later without any line moving.
pub fn relabel(g: &mut LegGrid, beats: i64) {
    let bar = g.bar();
    if bar <= 0 || g.beats.is_empty() {
        return;
    }
    let beats = ((beats % bar) + bar) % bar;
    if beats == 0 {
        return;
    }
    for (i, b) in g.beats.iter_mut().enumerate() {
        let n = if b.0 == 0 {
            ((i as i64 - beats) % bar + bar) % bar + 1
        } else {
            ((b.0 - 1 - beats) % bar + bar) % bar + 1
        };
        b.0 = n;
    }
    g.refresh_firsts();
}

/// What arbitration did and concluded, for the grid event.
pub struct Outcome {
    pub consensus: Consensus,
}

/// arbitrate: compare BeatNet's grid with Beat This's, apply the two
/// deterministic fixes unless told not to, and record the verdict. `ours`
/// is changed in place.
pub fn arbitrate(ours: &mut LegGrid, theirs: &LegGrid, resid_stdev: f64, s: &Settings) -> Outcome {
    let vote = BeatThisVote {
        bpm: theirs.bpm,
        first_beat_ms: theirs.first_beat_ms,
        phase_vote: theirs.phase_vote,
        phase_agreement: theirs.phase_agreement,
        resid_stdev,
    };
    let mut reasons = compare(ours, theirs, s);
    let mut relabeled = false;
    let mut shift_ms = None;
    let mut relabel_beats = None;
    if s.arbitrate {
        if let Some(ms) = half_beat_shift_ms(ours, theirs, s) {
            shift(ours, ms);
            shift_ms = Some(ms);
        }
        reasons = compare(ours, theirs, s);
        if reasons.is_empty() {
            if let Some(beats) = phase_relabel(theirs, s) {
                relabel(ours, beats);
                relabeled = true;
                relabel_beats = Some(beats);
            }
        }
    }
    // A confident relabel intentionally moves our bar 1 away from the
    // checker's synthetic bar 1; the raw vote is the evidence for it, so
    // the pre-relabel comparison stands and the vote is not a dispute.
    if !relabeled {
        if let Some(d) = phase_dispute(theirs, s) {
            if !reasons.is_empty() {
                reasons.push_str("; ");
            }
            reasons.push_str(&d);
        }
    }
    let consensus = if reasons.is_empty() {
        Consensus {
            verdict: Verdict::Agreed,
            dispute: None,
            shift_ms,
            relabel_beats,
            beat_this: Some(vote),
            note: None,
        }
    } else {
        Consensus {
            verdict: Verdict::Disputed,
            dispute: Some(reasons),
            shift_ms,
            relabel_beats,
            beat_this: Some(vote),
            note: None,
        }
    };
    Outcome { consensus }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn grid(bpm: f64, first_ms: i64, n: usize) -> LegGrid {
        let period = 60000.0 / bpm;
        let beats = (0..n)
            .map(|i| {
                (
                    (i % 4) as i64 + 1,
                    first_ms + (i as f64 * period).round() as i64,
                )
            })
            .collect();
        let mut g = LegGrid {
            bpm,
            beats_per_bar: 4,
            beats,
            ..Default::default()
        };
        g.refresh_firsts();
        g
    }

    #[test]
    fn agreeing_grids_agree() {
        let s = Settings::default();
        assert_eq!(compare(&grid(126.0, 0, 64), &grid(126.0, 0, 64), &s), "");
    }

    #[test]
    fn half_beat_lock_shifts_onto_the_checker() {
        let s = Settings::default();
        let mut ours = grid(120.0, 0, 64); // period 500 ms
        let theirs = grid(120.0, 250, 64);
        let out = arbitrate(&mut ours, &theirs, 0.0, &s);
        assert_eq!(out.consensus.shift_ms, Some(250));
        assert_eq!(ours.beats[0].1, 250);
        assert_eq!(out.consensus.verdict, Verdict::Agreed);
    }

    #[test]
    fn vote_relabels_at_the_settings_threshold() {
        let s = Settings::default();
        let mut ours = grid(126.0, 0, 64);
        let mut theirs = grid(126.0, 0, 64);
        theirs.phase_vote = 2;
        theirs.phase_agreement = 0.75;
        let out = arbitrate(&mut ours, &theirs, 0.0, &s);
        assert_eq!(out.consensus.relabel_beats, Some(2));
        assert_eq!(out.consensus.verdict, Verdict::Agreed);
        assert_eq!(ours.beats[2].0, 1);
        assert_eq!(ours.first_downbeat_ms, Some(ours.beats[2].1));

        let mut ours = grid(126.0, 0, 64);
        theirs.phase_agreement = 0.65;
        let out = arbitrate(&mut ours, &theirs, 0.0, &s);
        assert_eq!(out.consensus.relabel_beats, None);
        assert_eq!(out.consensus.verdict, Verdict::Disputed);
        assert_eq!(
            out.consensus.dispute.as_deref(),
            Some("downbeat vote +2 beats (0.65 agree)")
        );
    }

    #[test]
    fn vote_past_half_bar_reads_negative() {
        let s = Settings::default();
        let mut theirs = grid(126.0, 0, 8);
        theirs.phase_vote = 3;
        theirs.phase_agreement = 0.76;
        assert_eq!(
            phase_dispute(&theirs, &s).unwrap(),
            "downbeat vote -1 beats (0.76 agree)"
        );
    }

    #[test]
    fn arbitration_off_only_flags() {
        let s = Settings {
            arbitrate: false,
            ..Settings::default()
        };
        let mut ours = grid(120.0, 0, 64);
        let theirs = grid(120.0, 250, 64);
        let out = arbitrate(&mut ours, &theirs, 0.0, &s);
        assert_eq!(out.consensus.verdict, Verdict::Disputed);
        assert_eq!(ours.beats[0].1, 0);
    }
}
