//! The wave in flight is the stored detail wave, near enough not to notice
//! the swap when the grid lands: the frames, assembled and each band scaled
//! to its loudest column, against the three-band detail scaled the same,
//! at a browser row's resolution (112 bins). The frames' scale is fixed and
//! the record's is the track's; scaling both the same cancels that and
//! leaves the shaping, which is what this holds.

use std::path::Path;

const FIXTURES: &str = concat!(env!("CARGO_MANIFEST_DIR"), "/tests/fixtures");

fn wav_samples(path: &Path) -> Vec<i16> {
    let bytes = std::fs::read(path).unwrap();
    bytes[44..]
        .chunks_exact(2)
        .map(|b| i16::from_le_bytes([b[0], b[1]]))
        .collect()
}

/// Each band scaled so its loudest column is 127, then the peak per bin.
fn bins(data: &[u8], bins: usize) -> [Vec<f64>; 3] {
    let columns = data.len() / 3;
    let mut maxes = [0u8; 3];
    for c in data.chunks_exact(3) {
        for (b, m) in maxes.iter_mut().enumerate() {
            *m = (*m).max(c[b]);
        }
    }
    let mut out: [Vec<f64>; 3] = [vec![0.0; bins], vec![0.0; bins], vec![0.0; bins]];
    for (i, c) in data.chunks_exact(3).enumerate() {
        let bin = i * bins / columns.max(1);
        for b in 0..3 {
            let v = if maxes[b] > 0 {
                f64::from(c[b]) / f64::from(maxes[b])
            } else {
                0.0
            };
            out[b][bin] = out[b][bin].max(v);
        }
    }
    out
}

#[test]
fn frames_are_the_detail_wave_near_enough() {
    let samples = wav_samples(Path::new(&format!("{FIXTURES}/synthetic.wav")));
    let mut renderer = wave::Renderer::new(44100);
    let mut frames = Vec::new();
    for chunk in samples.chunks(10007) {
        frames.extend(renderer.push(chunk));
    }
    let (tail, rendered) = renderer.finish();
    frames.extend(tail);
    let flight: Vec<u8> = frames.iter().flat_map(|f| f.data.iter().copied()).collect();
    let detail = rendered
        .waves
        .iter()
        .find(|w| w.kind == "three_band_detail")
        .unwrap();
    assert_eq!(
        flight.len(),
        detail.data.len(),
        "the frames add up to the detail's width"
    );
    let a = bins(&flight, 112);
    let b = bins(&detail.data, 112);
    for band in 0..3 {
        let diffs: Vec<f64> = a[band]
            .iter()
            .zip(&b[band])
            .map(|(x, y)| (x - y).abs())
            .collect();
        let mean = diffs.iter().sum::<f64>() / diffs.len() as f64;
        let worst = diffs.iter().cloned().fold(0.0, f64::max);
        eprintln!("band {band}: mean {mean:.3} worst {worst:.3}");
        assert!(mean < 0.04, "band {band}: mean {mean}");
        assert!(worst < 0.11, "band {band}: worst {worst}");
    }
}

/// Rewrites the synthetic golden's frames from this renderer: the frames
/// are deadca7's own since 2026-10-08, where the rest of the golden is the
/// Go's. `cargo test -p wave --test fidelity regen -- --ignored`.
#[test]
#[ignore]
fn regen_synthetic_frames() {
    let path = format!("{FIXTURES}/synthetic.json");
    let mut gold: serde_json::Value =
        serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
    let samples = wav_samples(Path::new(&format!("{FIXTURES}/synthetic.wav")));
    let mut renderer = wave::Renderer::new(44100);
    let mut frames = Vec::new();
    for chunk in samples.chunks(10007) {
        frames.extend(renderer.push(chunk));
    }
    let (tail, _) = renderer.finish();
    frames.extend(tail);
    gold["frames"] = serde_json::Value::Array(
        frames
            .iter()
            .map(|f| {
                serde_json::json!({
                    "offset": f.offset,
                    "columns": f.columns,
                    "data": f.data.iter().map(|b| format!("{b:02x}")).collect::<String>(),
                })
            })
            .collect(),
    );
    std::fs::write(&path, serde_json::to_vec_pretty(&gold).unwrap()).unwrap();
}
