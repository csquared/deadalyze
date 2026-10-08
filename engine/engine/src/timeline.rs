//! Where an analysis's times count from: rekordbox's timeline. For an MP3
//! that is the whole decode, LAME's encoder delay and the decoder's 529
//! samples included; ffmpeg's default decode and the loaders the beat
//! models read with trim exactly the stream's `start_time`. rekordbox
//! keeps it, so the engine adds it to every grid time it publishes, and
//! decodes the waves and features with `-flags2 +skip_manual`, which puts
//! them on the same timeline with nothing to add. Other formats are 0
//! until measured against a rekordbox reference. (deadcatalog
//! `analysis/timeline`, ported.)

use anyhow::{anyhow, Context, Result};
use std::path::Path;
use std::process::Command;

pub const REKORDBOX: &str = "rekordbox";

pub fn has_encoder_delay(path: &Path) -> bool {
    path.extension()
        .map(|e| e.eq_ignore_ascii_case("mp3"))
        .unwrap_or(false)
}

/// The ffmpeg input flags (before -i) that decode a file on rekordbox's
/// timeline.
pub fn decoder_flags(path: &Path) -> Vec<String> {
    if has_encoder_delay(path) {
        vec!["-flags2".into(), "+skip_manual".into()]
    } else {
        vec![]
    }
}

/// The stream's start_time in seconds: how far rekordbox's timeline runs
/// ahead of the trimmed decode. 0 for formats without a measured delay.
pub fn start_time_s(ffprobe: &Path, path: &Path) -> Result<f64> {
    if !has_encoder_delay(path) {
        return Ok(0.0);
    }
    let out = Command::new(ffprobe)
        .args([
            "-v",
            "error",
            "-select_streams",
            "a:0",
            "-show_entries",
            "stream=start_time",
            "-of",
            "default=noprint_wrappers=1:nokey=1",
        ])
        .arg(path)
        .output()
        .with_context(|| format!("run {}", ffprobe.display()))?;
    if !out.status.success() {
        return Err(anyhow!(
            "read {}'s encoder delay: {}: {}",
            path.display(),
            out.status,
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    parse_start_time(&String::from_utf8_lossy(&out.stdout))
}

pub fn parse_start_time(out: &str) -> Result<f64> {
    let first = out.trim().split('\n').next().unwrap_or("").trim();
    let s = first.trim_end_matches([',', ' ']);
    if s.is_empty() || s == "N/A" {
        return Ok(0.0);
    }
    let seconds: f64 = s.parse().with_context(|| format!("encoder delay {s:?}"))?;
    if !(0.0..=1.0).contains(&seconds) {
        return Err(anyhow!("encoder delay {s:?} is not a delay"));
    }
    Ok(seconds)
}

/// The container's duration in ms, for a first paint before the decode.
pub fn duration_ms(ffprobe: &Path, path: &Path) -> Option<u32> {
    let out = Command::new(ffprobe)
        .args([
            "-v",
            "error",
            "-show_entries",
            "format=duration",
            "-of",
            "default=noprint_wrappers=1:nokey=1",
        ])
        .arg(path)
        .output()
        .ok()?;
    let s = String::from_utf8_lossy(&out.stdout);
    let seconds: f64 = s.trim().parse().ok()?;
    Some((seconds * 1000.0).round() as u32)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn start_time_parsing() {
        assert_eq!(parse_start_time("0.025057\n").unwrap(), 0.025057);
        assert_eq!(parse_start_time("0.025057,\n").unwrap(), 0.025057);
        assert_eq!(parse_start_time("N/A").unwrap(), 0.0);
        assert_eq!(parse_start_time("").unwrap(), 0.0);
        assert!(parse_start_time("2.5").is_err());
    }

    #[test]
    fn flags_for_mp3_only() {
        assert_eq!(
            decoder_flags(Path::new("a.MP3")),
            vec!["-flags2", "+skip_manual"]
        );
        assert!(decoder_flags(Path::new("a.aiff")).is_empty());
    }
}
