//! Goldens produced by deadcatalog's Go renderer (analysis/waveform) on the
//! same input: a 3 s synthetic signal (100 Hz, 1 kHz and 6 kHz sines under a
//! 0.5 Hz envelope) as 44.1 kHz mono s16 WAV. Every byte is compared within
//! ±1 (Go and Rust differ in fused multiply-add and libm rounding), and the
//! metadata exactly.
//!
//! The file path (`render_file`) runs when `WAVE_FFMPEG` names an ffmpeg, or
//! deadcatalog's runtime copy is installed. A real-track comparison runs
//! when `WAVE_GOLD_MP3` (a JSON the Go dumped) and `WAVE_MP3` are set.

use std::path::{Path, PathBuf};

use serde_json::Value;

const FIXTURES: &str = concat!(env!("CARGO_MANIFEST_DIR"), "/tests/fixtures");

fn unhex(s: &str) -> Vec<u8> {
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap())
        .collect()
}

fn wav_samples(path: &Path) -> Vec<i16> {
    let bytes = std::fs::read(path).unwrap();
    assert_eq!(&bytes[0..4], b"RIFF");
    assert_eq!(&bytes[36..40], b"data");
    bytes[44..]
        .chunks_exact(2)
        .map(|p| i16::from_le_bytes([p[0], p[1]]))
        .collect()
}

/// Byte-for-byte diff statistics between a Rust wave and a Go one.
#[derive(Default, Debug)]
struct Diff {
    bytes: usize,
    differing: usize,
    max: u32,
}

impl Diff {
    fn add(&mut self, got: &[u8], want: &[u8]) {
        assert_eq!(got.len(), want.len(), "length");
        for (&g, &w) in got.iter().zip(want) {
            let d = g.abs_diff(w) as u32;
            self.bytes += 1;
            if d > 0 {
                self.differing += 1;
            }
            self.max = self.max.max(d);
        }
    }
}

fn compare(got: &wave::Rendered, frames: Option<&[wave::Frame]>, gold: &Value, label: &str) {
    assert_eq!(
        got.sample_rate,
        gold["sample_rate"].as_u64().unwrap() as u32
    );
    assert_eq!(
        got.duration_ms,
        gold["duration_ms"].as_u64().unwrap() as u32,
        "duration"
    );
    let scales: Vec<u16> = gold["band_scales"]
        .as_array()
        .unwrap()
        .iter()
        .map(|v| v.as_u64().unwrap() as u16)
        .collect();
    let mut total = Diff::default();
    let gold_waves = gold["waves"].as_array().unwrap();
    assert_eq!(got.waves.len(), gold_waves.len());
    for (w, g) in got.waves.iter().zip(gold_waves) {
        assert_eq!(w.kind, g["kind"].as_str().unwrap());
        assert_eq!(
            w.entry_bytes as u64,
            g["entry_bytes"].as_u64().unwrap(),
            "{}",
            w.kind
        );
        assert_eq!(
            w.entry_count as u64,
            g["entry_count"].as_u64().unwrap(),
            "{}",
            w.kind
        );
        assert_eq!(w.rate as u64, g["rate"].as_u64().unwrap(), "{}", w.kind);
        assert_eq!(w.source, g["source"].as_str().unwrap());
        let want = unhex(g["data"].as_str().unwrap());
        let mut d = Diff::default();
        d.add(&w.data, &want);
        eprintln!(
            "{label} {:<20} {:>7} bytes, {:>6} differ ({:.4}%), max diff {}",
            w.kind,
            d.bytes,
            d.differing,
            100.0 * d.differing as f64 / d.bytes as f64,
            d.max
        );
        assert!(d.max <= 1, "{label} {}: max diff {}", w.kind, d.max);
        total.add(&w.data, &want);
    }
    // The scales are compared exactly: a ±1 on a scale would shift a whole
    // lane, and the Go's rounding is the same as ours here.
    assert_eq!(got.band_scales.to_vec(), scales, "{label} band scales");
    if let Some(frames) = frames {
        let gold_frames = gold["frames"].as_array().unwrap();
        assert_eq!(frames.len(), gold_frames.len(), "{label} frame count");
        let mut d = Diff::default();
        for (f, g) in frames.iter().zip(gold_frames) {
            assert_eq!(f.offset as u64, g["offset"].as_u64().unwrap());
            assert_eq!(f.columns as u64, g["columns"].as_u64().unwrap());
            d.add(&f.data, &unhex(g["data"].as_str().unwrap()));
        }
        eprintln!(
            "{label} {:<20} {:>7} bytes, {:>6} differ ({:.4}%), max diff {}",
            "frames",
            d.bytes,
            d.differing,
            100.0 * d.differing as f64 / d.bytes as f64,
            d.max
        );
        assert!(d.max <= 1, "{label} frames: max diff {}", d.max);
        total.add(
            &frames
                .iter()
                .flat_map(|f| f.data.iter().copied())
                .collect::<Vec<_>>(),
            &gold_frames
                .iter()
                .flat_map(|g| unhex(g["data"].as_str().unwrap()))
                .collect::<Vec<_>>(),
        );
    }
    eprintln!(
        "{label} TOTAL {} bytes, {} differ ({:.4}%), max diff {}",
        total.bytes,
        total.differing,
        100.0 * total.differing as f64 / total.bytes as f64,
        total.max
    );
}

fn ffmpeg() -> Option<PathBuf> {
    if let Ok(p) = std::env::var("WAVE_FFMPEG") {
        return Some(PathBuf::from(p));
    }
    let home = std::env::var_os("HOME")?;
    let p = PathBuf::from(home)
        .join("Library/Application Support/deadcatalog/runtime/current/tools/ffmpeg");
    p.is_file().then_some(p)
}

#[test]
fn synthetic_matches_go_in_memory() {
    let gold: Value =
        serde_json::from_slice(&std::fs::read(format!("{FIXTURES}/synthetic.json")).unwrap())
            .unwrap();
    let samples = wav_samples(Path::new(&format!("{FIXTURES}/synthetic.wav")));
    assert_eq!(samples.len(), 3 * 44100);

    let mut renderer = wave::Renderer::new(44100);
    let mut frames = Vec::new();
    for chunk in samples.chunks(10007) {
        frames.extend(renderer.push(chunk));
    }
    let (tail, rendered) = renderer.finish();
    frames.extend(tail);
    compare(&rendered, Some(&frames), &gold, "synthetic/memory");
}

#[test]
fn synthetic_matches_go_through_ffmpeg() {
    let Some(ffmpeg) = ffmpeg() else {
        eprintln!("no ffmpeg (set WAVE_FFMPEG); skipping");
        return;
    };
    let gold: Value =
        serde_json::from_slice(&std::fs::read(format!("{FIXTURES}/synthetic.json")).unwrap())
            .unwrap();
    let mut frames = Vec::new();
    let rendered = wave::render_file(
        &ffmpeg,
        &[],
        Path::new(&format!("{FIXTURES}/synthetic.wav")),
        &mut |f| frames.push(f),
    )
    .unwrap();
    compare(&rendered, Some(&frames), &gold, "synthetic/ffmpeg");
}

#[test]
fn real_mp3_matches_go() {
    let (Some(ffmpeg), Ok(gold_path), Ok(mp3)) = (
        ffmpeg(),
        std::env::var("WAVE_GOLD_MP3"),
        std::env::var("WAVE_MP3"),
    ) else {
        eprintln!("set WAVE_GOLD_MP3 and WAVE_MP3 to compare a real track; skipping");
        return;
    };
    let gold: Value = serde_json::from_slice(&std::fs::read(gold_path).unwrap()).unwrap();
    let mut frames = Vec::new();
    let rendered = wave::render_file(
        &ffmpeg,
        &["-flags2", "+skip_manual"],
        Path::new(&mp3),
        &mut |f| frames.push(f),
    )
    .unwrap();
    compare(&rendered, Some(&frames), &gold, "12.mp3");
}

#[test]
fn render_file_reports_ffmpeg_failures() {
    let Some(ffmpeg) = ffmpeg() else {
        return;
    };
    let err = wave::render_file(
        &ffmpeg,
        &[],
        Path::new("/nonexistent/track.flac"),
        &mut |_| {},
    )
    .unwrap_err();
    let msg = err.to_string();
    assert!(msg.starts_with("decode waveform audio: "), "{msg}");
    assert!(msg.contains("nonexistent"), "{msg}");
}
