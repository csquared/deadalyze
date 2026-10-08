//! deadca7's waveform renderer, ported from deadcatalog's Go
//! (`analysis/waveform` and `analysis/wavecolor`): mono 44.1 kHz PCM in,
//! rekordbox's seven waves out, with provisional three-band frames streamed
//! while the audio is still arriving.
//!
//! [`Renderer`] takes samples incrementally ([`Renderer::push`]) and hands
//! back each completed second of provisional columns; [`Renderer::finish`]
//! flushes the tail and builds the record. [`render_file`] drives it from an
//! ffmpeg decode, with the same arguments the Go uses.
//!
//! The arithmetic (biquad structure, evaluation order, rounding and
//! truncation) follows the Go so the bytes match to within rounding.

mod analyze;
mod color;
mod filter;

use std::io::Read;
use std::path::Path;
use std::process::{Command, Stdio};

use anyhow::{anyhow, Context};

pub use analyze::{detail_width, BandColumn, COLOR_WIDTH, DETAIL_RATE, PREVIEW_WIDTH};

/// Columns per second in the detail waves and the provisional frames.
pub const COLUMNS_PER_SECOND: u32 = 150;

/// The sample rate `render_file` decodes to, and the one rekordbox's waves
/// are measured at.
pub const DECODE_SAMPLE_RATE: u32 = 44100;

/// The most of ffmpeg's stderr an error carries.
const MAX_FFMPEG_STDERR: usize = 4096;

/// A run of three-band columns at `COLUMNS_PER_SECOND`, handed out while the
/// audio is still arriving: `offset` is the index of its first column in the
/// track, `data` is `columns * 3` bytes (low, mid, high) on the 0..127 scale
/// the stored detail wave uses.
///
/// The columns are provisional: the stored detail wave is scaled against
/// the whole track's peaks, which nothing knows until the last sample; a
/// frame is scaled against a fixed level (6 dB of gain, clipped) instead.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Frame {
    pub offset: u32,
    pub columns: u32,
    pub data: Vec<u8>,
}

/// One stored wave, with the metadata deadcatalog's catalog keeps for it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Wave {
    pub kind: &'static str,
    pub entry_bytes: u32,
    pub entry_count: u32,
    /// Columns per second for the detail waves, 0 for the previews.
    pub rate: u32,
    pub source: &'static str,
    pub data: Vec<u8>,
}

/// The record: the display scales, the duration and the seven waves, in the
/// Go's order (mono_preview, mono_detail, color_preview, color_detail,
/// three_band_preview, three_band_detail, three_band_scales).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Rendered {
    pub band_scales: [u16; 3],
    pub duration_ms: u32,
    pub sample_rate: u32,
    pub waves: Vec<Wave>,
}

impl Rendered {
    /// The wave of a kind, if present.
    pub fn wave(&self, kind: &str) -> Option<&Wave> {
        self.waves.iter().find(|w| w.kind == kind)
    }
}

/// Renders a track from samples fed in order: every sample is kept for the
/// record, and the five band filters run once as they arrive, feeding both
/// the provisional frames and the per-millisecond band source.
#[derive(Clone, Debug)]
pub struct Renderer {
    sample_rate: u32,
    samples: Vec<i16>,
    filters: filter::Bands,
    source: analyze::SourceBuilder,
    // The provisional frame being drawn.
    samples_per_column: usize,
    column: [f64; 3],
    in_column: usize,
    columns: Vec<u8>,
    emitted: usize,
}

/// Columns in a frame: a second's worth.
const FRAME_COLUMNS: usize = COLUMNS_PER_SECOND as usize;

impl Renderer {
    /// A renderer for mono samples at `sample_rate` Hz.
    ///
    /// # Panics
    ///
    /// If `sample_rate` is 0.
    pub fn new(sample_rate: u32) -> Self {
        assert!(sample_rate > 0, "a sample rate is required");
        Renderer {
            sample_rate,
            samples: Vec::new(),
            filters: filter::Bands::new(sample_rate),
            source: analyze::SourceBuilder::new(sample_rate),
            samples_per_column: (sample_rate as usize / FRAME_COLUMNS).max(1),
            column: [0.0; 3],
            in_column: 0,
            columns: Vec::with_capacity(FRAME_COLUMNS * 3),
            emitted: 0,
        }
    }

    /// Push mono i16 samples; returns any completed provisional frames
    /// (about one second each) in order.
    pub fn push(&mut self, samples: &[i16]) -> Vec<Frame> {
        let mut frames = Vec::new();
        self.samples.extend_from_slice(samples);
        for &sample in samples {
            let bands = self.filters.process(f64::from(sample) / 32768.0);
            self.source.push(bands);
            self.column[0] = f64::max(self.column[0], bands[0]);
            self.column[1] = f64::max(self.column[1], bands[1]);
            self.column[2] = f64::max(self.column[2], bands[2]);
            self.in_column += 1;
            if self.in_column >= self.samples_per_column {
                self.close_column(&mut frames);
            }
        }
        frames
    }

    fn close_column(&mut self, frames: &mut Vec<Frame>) {
        self.columns.extend_from_slice(&[
            provisional_byte(self.column[0]),
            provisional_byte(self.column[1]),
            provisional_byte(self.column[2]),
        ]);
        self.column = [0.0; 3];
        self.in_column = 0;
        if self.columns.len() / 3 >= FRAME_COLUMNS {
            self.emit(frames);
        }
    }

    fn emit(&mut self, frames: &mut Vec<Frame>) {
        let columns = self.columns.len() / 3;
        frames.push(Frame {
            offset: self.emitted as u32,
            columns: columns as u32,
            data: std::mem::take(&mut self.columns),
        });
        self.emitted += columns;
    }

    /// Finish: flush the last partial frame (a short last column included)
    /// and build the seven waves. The frames handed out over the whole track
    /// add up to exactly the detail width.
    pub fn finish(mut self) -> (Vec<Frame>, Rendered) {
        let mut frames = Vec::new();
        if self.in_column > 0 {
            self.close_column(&mut frames);
        }
        if !self.columns.is_empty() {
            self.emit(&mut frames);
        }
        let source = if self.sample_rate >= 1000 {
            self.source.finish()
        } else {
            analyze::band_source(&self.samples, self.sample_rate)
        };
        let rendered = render(&self.samples, self.sample_rate, &source);
        (frames, rendered)
    }
}

/// Puts a band peak (0..1 of full scale) on the stored wave's 0..127 with
/// 6 dB of gain.
fn provisional_byte(peak: f64) -> u8 {
    analyze::clamp_byte((f64::min(1.0, peak * 2.0) * 127.0).round())
}

/// The Go's `AnalyzeSamples`, given the band source already reduced.
fn render(samples: &[i16], sample_rate: u32, source: &[BandColumn]) -> Rendered {
    let duration_ms = (samples.len() as f64 / f64::from(sample_rate) * 1000.0).round() as u32;
    let width = detail_width(samples.len(), sample_rate);

    let mono_detail = analyze::analyze_mono(samples, width);
    let mono_preview = analyze::resample_bytes(&mono_detail, PREVIEW_WIDTH);
    let averages = analyze::preview_averages(source);
    let norm = analyze::PreviewNorm::from_averages(&averages);
    let band_scales = analyze::band_scales(source, &norm);
    let three_band_preview = analyze::preview_from_averages(&averages, &norm);
    let three_band_preview = analyze::preview_hold(&three_band_preview, [0.8, 0.85, 0.0]);
    let three_band_preview = analyze::preview_blend(&three_band_preview);
    let three_band_detail = analyze::scroll(source, width, band_scales);
    let color_preview = color::preview(&three_band_detail);
    let color_detail = color::detail(&three_band_detail, &mono_detail, band_scales);

    let wave = |kind, entry_bytes: u32, rate, source, data: Vec<u8>| Wave {
        kind,
        entry_bytes,
        entry_count: (data.len() / entry_bytes as usize) as u32,
        rate,
        source,
        data,
    };
    let rate = COLUMNS_PER_SECOND;
    Rendered {
        band_scales,
        duration_ms,
        sample_rate,
        waves: vec![
            wave("mono_preview", 1, 0, "dat", mono_preview),
            wave("mono_detail", 1, rate, "ext", mono_detail),
            wave("color_preview", 6, 0, "ext", color_preview),
            wave("color_detail", 2, rate, "ext", color_detail),
            wave("three_band_preview", 3, 0, "twoex", three_band_preview),
            wave("three_band_detail", 3, rate, "twoex", three_band_detail),
            wave(
                "three_band_scales",
                2,
                0,
                "twoex",
                analyze::encode_band_scales(band_scales),
            ),
        ],
    }
}

/// Renders a track in one go from samples already in memory.
pub fn render_samples(sample_rate: u32, samples: &[i16]) -> Rendered {
    let mut renderer = Renderer::new(sample_rate);
    renderer.push(samples);
    renderer.finish().1
}

/// The arguments ffmpeg is run with: `-nostdin -v error [decoder flags] -i
/// PATH -ac 1 -ar 44100 -f s16le -acodec pcm_s16le pipe:1`.
pub fn ffmpeg_args(decoder_flags: &[&str], path: &Path) -> Vec<std::ffi::OsString> {
    let mut args: Vec<std::ffi::OsString> = vec!["-nostdin".into(), "-v".into(), "error".into()];
    args.extend(decoder_flags.iter().map(|f| f.into()));
    args.push("-i".into());
    args.push(path.into());
    for a in [
        "-ac",
        "1",
        "-ar",
        "44100",
        "-f",
        "s16le",
        "-acodec",
        "pcm_s16le",
        "pipe:1",
    ] {
        args.push(a.into());
    }
    args
}

/// Decodes `path` with `ffmpeg` to mono 44.1 kHz s16le (the decoder flags,
/// such as `-flags2 +skip_manual` for an MP3, go before `-i`), streams each
/// second of provisional columns to `on_frame` as it is drawn, and returns
/// the record once the whole track has gone by.
pub fn render_file(
    ffmpeg: &Path,
    decoder_flags: &[&str],
    path: &Path,
    on_frame: &mut dyn FnMut(Frame),
) -> anyhow::Result<Rendered> {
    let mut child = Command::new(ffmpeg)
        .args(ffmpeg_args(decoder_flags, path))
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .with_context(|| format!("decode waveform audio: {}", ffmpeg.display()))?;
    let mut stderr = child.stderr.take().expect("stderr is piped");
    let stderr_reader = std::thread::spawn(move || {
        let mut buf = Vec::new();
        let _ = stderr.read_to_end(&mut buf);
        buf
    });
    let mut stdout = child.stdout.take().expect("stdout is piped");

    let mut renderer = Renderer::new(DECODE_SAMPLE_RATE);
    let read_err = stream_samples(&mut stdout, &mut renderer, on_frame).err();
    drop(stdout);
    let status = child.wait();
    let stderr = stderr_reader.join().unwrap_or_default();
    match status {
        Ok(status) if status.success() => {}
        Ok(status) => return Err(ffmpeg_error(&status.to_string(), &stderr)),
        Err(err) => return Err(ffmpeg_error(&err.to_string(), &stderr)),
    }
    if let Some(err) = read_err {
        return Err(anyhow!("decode waveform audio: {err}"));
    }
    if renderer.samples.is_empty() {
        return Err(anyhow!("{}: decoded no waveform samples", path.display()));
    }
    let (frames, rendered) = renderer.finish();
    for frame in frames {
        on_frame(frame);
    }
    Ok(rendered)
}

/// Reads s16le mono from `r` into the renderer, a second at a time, handing
/// each completed frame to `on_frame`. A trailing odd byte is dropped.
fn stream_samples(
    r: &mut dyn Read,
    renderer: &mut Renderer,
    on_frame: &mut dyn FnMut(Frame),
) -> std::io::Result<()> {
    let mut buf = vec![0u8; DECODE_SAMPLE_RATE as usize * 2];
    let mut carried: Option<u8> = None;
    let mut samples = Vec::with_capacity(DECODE_SAMPLE_RATE as usize);
    loop {
        let n = match r.read(&mut buf) {
            Ok(0) => break,
            Ok(n) => n,
            Err(err) if err.kind() == std::io::ErrorKind::Interrupted => continue,
            Err(err) => return Err(err),
        };
        samples.clear();
        let mut bytes = &buf[..n];
        if let Some(low) = carried.take() {
            samples.push(i16::from_le_bytes([low, bytes[0]]));
            bytes = &bytes[1..];
        }
        let mut pairs = bytes.chunks_exact(2);
        samples.extend(pairs.by_ref().map(|p| i16::from_le_bytes([p[0], p[1]])));
        if let [odd] = pairs.remainder() {
            carried = Some(*odd);
        }
        for frame in renderer.push(&samples) {
            on_frame(frame);
        }
    }
    Ok(())
}

fn ffmpeg_error(err: &str, stderr: &[u8]) -> anyhow::Error {
    let stderr = String::from_utf8_lossy(stderr);
    let stderr = stderr.trim();
    if stderr.is_empty() {
        return anyhow!("decode waveform audio: {err}");
    }
    let tail = if stderr.len() > MAX_FFMPEG_STDERR {
        let cut = stderr.len() - MAX_FFMPEG_STDERR;
        let cut = (cut..stderr.len())
            .find(|&i| stderr.is_char_boundary(i))
            .unwrap_or(stderr.len());
        &stderr[cut..]
    } else {
        stderr
    };
    anyhow!("decode waveform audio: {err}: {tail}")
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::f64::consts::PI;

    fn distinct_bytes(values: &[u8]) -> usize {
        let mut seen = [false; 256];
        for &v in values {
            seen[v as usize] = true;
        }
        seen.iter().filter(|&&s| s).count()
    }

    // Go's TestAnalyzeSamplesBuildsCatalogWaveforms.
    #[test]
    fn builds_catalog_waveforms() {
        let sr = 22050u32;
        let samples: Vec<i16> = (0..2 * 22050)
            .map(|i| {
                let t = i as f64 / 22050.0;
                let v = 0.40 * (2.0 * PI * 70.0 * t).sin()
                    + 0.25 * (2.0 * PI * 1200.0 * t).sin()
                    + 0.15 * (2.0 * PI * 8000.0 * t).sin();
                (v * 32767.0).round() as i16
            })
            .collect();
        let got = render_samples(sr, &samples);
        assert_eq!(got.duration_ms, 2000);
        assert_eq!(got.sample_rate, sr);
        assert_ne!(got.band_scales, [0, 0, 0]);
        let width = detail_width(samples.len(), sr);
        let kinds: Vec<&str> = got.waves.iter().map(|w| w.kind).collect();
        assert_eq!(
            kinds,
            [
                "mono_preview",
                "mono_detail",
                "color_preview",
                "color_detail",
                "three_band_preview",
                "three_band_detail",
                "three_band_scales"
            ]
        );
        let data = |kind: &str| &got.wave(kind).unwrap().data;
        assert_eq!(data("mono_preview").len(), PREVIEW_WIDTH);
        assert_eq!(data("mono_detail").len(), PREVIEW_WIDTH);
        assert_eq!(data("color_preview").len(), COLOR_WIDTH * 6);
        assert_eq!(data("three_band_preview").len(), COLOR_WIDTH * 3);
        assert_eq!(data("three_band_detail").len(), width * 3);
        assert_eq!(data("three_band_scales").len(), 6);
        assert_eq!(data("color_detail").len(), width * 2);
        assert!(distinct_bytes(data("three_band_detail")) > 4);
        assert_eq!(
            &color::preview(data("three_band_detail")),
            data("color_preview")
        );
        assert_eq!(
            &color::detail(
                data("three_band_detail"),
                data("mono_detail"),
                got.band_scales
            ),
            data("color_detail")
        );
        for w in &got.waves {
            assert_eq!(
                w.data.len(),
                (w.entry_bytes * w.entry_count) as usize,
                "{}",
                w.kind
            );
        }
        assert_eq!(got.wave("mono_detail").unwrap().rate, 150);
        assert_eq!(got.wave("mono_preview").unwrap().rate, 0);
        assert_eq!(got.wave("mono_preview").unwrap().source, "dat");
    }

    // Go's TestStreamSamplesHandsOutTheTrackAsItDecodes: a frame per second
    // of columns, in order, adding up to exactly the detail width, with a
    // short last column when the track does not end on one.
    #[test]
    fn hands_out_the_track_as_it_arrives() {
        let sr = 22050u32;
        let n = 22050 * 2 + 22050 / 2; // 375 columns of 147
        let pcm: Vec<i16> = (0..n)
            .map(|i| {
                let t = i as f64 / 22050.0;
                let v = 0.5 * (2.0 * PI * 70.0 * t).sin()
                    + 0.3 * (2.0 * PI * 1200.0 * t).sin()
                    + 0.2 * (2.0 * PI * 8000.0 * t).sin();
                (v * 32767.0) as i16
            })
            .collect();

        // Fed in uneven pieces, the frames are the same as fed at once.
        let mut renderer = Renderer::new(sr);
        let mut frames = Vec::new();
        for chunk in pcm.chunks(1234) {
            frames.extend(renderer.push(chunk));
        }
        let (tail, rendered) = renderer.finish();
        frames.extend(tail);

        assert_eq!(frames.len(), 3, "a frame per second, and the tail");
        assert_eq!(frames[0].columns, 150);
        assert_eq!(frames[0].offset, 0);
        assert_eq!(frames[1].offset, 150);
        assert_eq!(frames[2].offset, 300);
        let mut total = 0;
        for f in &frames {
            assert_eq!(f.data.len(), f.columns as usize * 3);
            total += f.columns as usize;
        }
        assert_eq!(total, detail_width(n, sr));
        assert!(
            distinct_bytes(&frames[0].data) > 4,
            "a picture, not a flat line"
        );

        let mut once = Renderer::new(sr);
        let mut at_once = once.push(&pcm);
        let (tail, rendered_once) = once.finish();
        at_once.extend(tail);
        assert_eq!(at_once, frames);
        assert_eq!(rendered_once, rendered);
    }

    // Go's TestStreamSamplesClosesAShortLastColumn.
    #[test]
    fn closes_a_short_last_column() {
        let sr = 22050u32;
        let n = 147 * 3 + 10;
        let pcm: Vec<i16> = (0..n)
            .map(|i| ((i as f64 / 7.0).sin() * 20000.0) as i16)
            .collect();
        let mut renderer = Renderer::new(sr);
        let mut frames = renderer.push(&pcm);
        frames.extend(renderer.finish().0);
        assert_eq!(frames.len(), 1);
        assert_eq!(frames[0].columns, 4);
        assert_eq!(frames[0].columns as usize, detail_width(n, sr));
    }

    // Go's TestDecodeArgsReadRekordboxsTimeline, with the flags the caller
    // passes in.
    #[test]
    fn ffmpeg_args_match_go() {
        let args = ffmpeg_args(&["-flags2", "+skip_manual"], Path::new("/m/track.mp3"));
        let args: Vec<&str> = args.iter().map(|a| a.to_str().unwrap()).collect();
        assert_eq!(
            args,
            [
                "-nostdin",
                "-v",
                "error",
                "-flags2",
                "+skip_manual",
                "-i",
                "/m/track.mp3",
                "-ac",
                "1",
                "-ar",
                "44100",
                "-f",
                "s16le",
                "-acodec",
                "pcm_s16le",
                "pipe:1"
            ]
        );
        let plain = ffmpeg_args(&[], Path::new("/m/track.flac"));
        assert_eq!(plain[3], "-i");
    }

    // Go's TestFFmpegErrorIncludesBoundedStderr.
    #[test]
    fn ffmpeg_error_includes_bounded_stderr() {
        let err = ffmpeg_error("exit status 1", b"  missing decoder\n");
        assert_eq!(
            err.to_string(),
            "decode waveform audio: exit status 1: missing decoder"
        );
        let long = vec![b'x'; MAX_FFMPEG_STDERR + 10];
        let err = ffmpeg_error("exit status 1", &long);
        assert!(
            err.to_string().len()
                <= "decode waveform audio: exit status 1: ".len() + MAX_FFMPEG_STDERR
        );
        let err = ffmpeg_error("exit status 1", b"   ");
        assert_eq!(err.to_string(), "decode waveform audio: exit status 1");
    }

    #[test]
    fn stream_samples_carries_an_odd_byte_across_reads() {
        // 5 samples as 10 bytes, read 3 bytes at a time.
        struct Dribble<'a>(&'a [u8]);
        impl Read for Dribble<'_> {
            fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
                let n = self.0.len().min(3).min(buf.len());
                buf[..n].copy_from_slice(&self.0[..n]);
                self.0 = &self.0[n..];
                Ok(n)
            }
        }
        let want: Vec<i16> = vec![1, -2, 300, -400, 32767];
        let bytes: Vec<u8> = want.iter().flat_map(|s| s.to_le_bytes()).collect();
        let mut renderer = Renderer::new(44100);
        stream_samples(&mut Dribble(&bytes), &mut renderer, &mut |_| {}).unwrap();
        assert_eq!(renderer.samples, want);
    }
}
