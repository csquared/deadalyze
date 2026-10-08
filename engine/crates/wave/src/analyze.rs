//! The waves themselves, ported function for function from deadcatalog's
//! `analysis/waveform` (Go): the mono energy wave, the per-millisecond band
//! source, the display scales, the 1200-column three-band preview and the
//! scrolling three-band detail. Integer division, truncation and rounding
//! follow the Go exactly; `round` is half away from zero in both.

use crate::filter::Bands;

/// Columns in the mono preview (PWV3's overview).
pub const PREVIEW_WIDTH: usize = 400;
/// Columns in the colour and three-band previews.
pub const COLOR_WIDTH: usize = 1200;
/// Columns per second in the detail waves.
pub const DETAIL_RATE: usize = 150;

// The preview's display numerators (the Go's previewLowMidNumerator and
// previewHighNumerator).
const PREVIEW_LOW_MID_NUMERATOR: f64 = 32768.0;
const PREVIEW_HIGH_NUMERATOR: f64 = 33376.0;

/// One column's band peaks, on the 0..32767 scale of the band source.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct BandColumn {
    pub low: f64,
    pub mid: f64,
    pub high: f64,
}

impl BandColumn {
    #[inline]
    fn lane(&self, lane: usize) -> f64 {
        match lane {
            0 => self.low,
            1 => self.mid,
            _ => self.high,
        }
    }
}

/// Columns in the detail waves for `sample_count` samples at `sample_rate`:
/// `ceil(samples * 150 / rate)`, at least 1.
pub fn detail_width(sample_count: usize, sample_rate: u32) -> usize {
    if sample_count == 0 || sample_rate == 0 {
        return 1;
    }
    ceil_div(sample_count * DETAIL_RATE, sample_rate as usize).max(1)
}

#[inline]
fn to_f64(sample: i16) -> f64 {
    f64::from(sample) / 32768.0
}

/// The mono energy wave: one byte a column (height in the low five bits, a
/// whiteness in the top three), at least `PREVIEW_WIDTH` columns.
pub fn analyze_mono(samples: &[i16], width: usize) -> Vec<u8> {
    let width = width.max(PREVIEW_WIDTH);
    let mut out = vec![0u8; width];
    if samples.is_empty() {
        return out;
    }
    let n = samples.len();
    for (i, column) in out.iter_mut().enumerate() {
        let start = i * n / width;
        let mut end = (i + 1) * n / width;
        if end <= start {
            end = start + 1;
        }
        if end > n {
            end = n;
        }
        let mut energy = 0.0;
        for &sample in &samples[start..end] {
            let s = to_f64(sample);
            energy += s * s;
        }
        *column = pack_mono_energy(energy / (end - start) as f64);
    }
    out
}

pub(crate) fn pack_mono_energy(energy: f64) -> u8 {
    if energy <= 0.0 || energy.is_nan() {
        return 0;
    }
    let mut height = f64::min(31.0, (energy * 31.0).round()) as u8;
    if height == 0 {
        height = 1;
    }
    let mut whiteness = 4u8;
    if height >= 24 {
        whiteness = 5;
    }
    if height >= 30 {
        whiteness = 6;
    }
    (whiteness << 5) | height
}

/// Gathers the band source as samples go by: each sample's band magnitudes
/// (0..1) are scaled to 0..32767 and reduced to a peak per millisecond. The
/// Go computes every sample's bands and then reduces; this reduces as it
/// goes, into the same buckets (`ceil(i*rate/1000) <= j < ceil((i+1)*rate/1000)`,
/// which is `i = j*1000/rate` for a rate of 1000 Hz or more).
#[derive(Clone, Debug)]
pub(crate) struct SourceBuilder {
    sample_rate: u64,
    count: u64,
    source: Vec<BandColumn>,
}

impl SourceBuilder {
    pub(crate) fn new(sample_rate: u32) -> Self {
        SourceBuilder {
            sample_rate: u64::from(sample_rate),
            count: 0,
            source: Vec::new(),
        }
    }

    #[inline]
    pub(crate) fn push(&mut self, bands: [f64; 3]) {
        let bucket = (self.count * 1000 / self.sample_rate) as usize;
        self.count += 1;
        if bucket >= self.source.len() {
            self.source.resize(bucket + 1, BandColumn::default());
        }
        let column = &mut self.source[bucket];
        column.low = f64::max(column.low, bands[0] * 32768.0);
        column.mid = f64::max(column.mid, bands[1] * 32768.0);
        column.high = f64::max(column.high, bands[2] * 32768.0);
    }

    /// The source: `samples*1000/rate` columns (at least one), each capped at
    /// 32767; the samples past the last whole millisecond are not counted,
    /// as in the Go.
    pub(crate) fn finish(mut self) -> Vec<BandColumn> {
        if self.count == 0 {
            return Vec::new();
        }
        let num_ms = ((self.count * 1000 / self.sample_rate) as usize).max(1);
        self.source.truncate(num_ms);
        self.source.resize(num_ms, BandColumn::default());
        for column in &mut self.source {
            column.low = f64::min(32767.0, column.low);
            column.mid = f64::min(32767.0, column.mid);
            column.high = f64::min(32767.0, column.high);
        }
        self.source
    }
}

/// The band source computed the Go way, all at once: the reference for
/// `SourceBuilder`, and the path for rates under 1000 Hz, whose buckets
/// overlap.
pub(crate) fn band_source(samples: &[i16], sample_rate: u32) -> Vec<BandColumn> {
    if samples.is_empty() || sample_rate == 0 {
        return Vec::new();
    }
    let sr = sample_rate as usize;
    let num_ms = (samples.len() * 1000 / sr).max(1);
    let mut filters = Bands::new(sample_rate);
    let bands: Vec<[f64; 3]> = samples
        .iter()
        .map(|&s| {
            let b = filters.process(to_f64(s));
            [b[0] * 32768.0, b[1] * 32768.0, b[2] * 32768.0]
        })
        .collect();
    let mut source = vec![BandColumn::default(); num_ms];
    for (i, column) in source.iter_mut().enumerate() {
        let mut start = ceil_div(i * sr, 1000);
        let mut end = ceil_div((i + 1) * sr, 1000);
        if end <= start {
            end = start + 1;
        }
        if start >= bands.len() {
            start = bands.len() - 1;
        }
        if end > bands.len() {
            end = bands.len();
        }
        for band in &bands[start..end] {
            column.low = f64::max(column.low, band[0]);
            column.mid = f64::max(column.mid, band[1]);
            column.high = f64::max(column.high, band[2]);
        }
        column.low = f64::min(32767.0, column.low);
        column.mid = f64::min(32767.0, column.mid);
        column.high = f64::min(32767.0, column.high);
    }
    source
}

/// The display scales of the three-band detail: the Go's `bandScales`.
pub(crate) fn band_scales(source: &[BandColumn], norm: &PreviewNorm) -> [u16; 3] {
    let mut maxes = [0.0f64; 3];
    for c in source {
        maxes[0] = f64::max(maxes[0], c.low);
        maxes[1] = f64::max(maxes[1], c.mid);
        maxes[2] = f64::max(
            maxes[2],
            64.0 - 64.0 * (c.high * std::f64::consts::PI / 32768.0).cos(),
        );
    }
    let raw = [
        scale_for_max(maxes[0], 127.0 * 25600.0, f64::round),
        scale_for_max(maxes[1], 128.0 * 25600.0, f64::trunc),
        scale_for_max(maxes[2], 12700.0, f64::ceil),
    ];
    let mut scales = raw;
    let caps = norm.scale_caps();
    let low_cap = linear_scale(maxes[0], caps[0], 3.0);
    scales[0] = apply_scale_cap(scales[0], low_cap, 5);
    if scales[0] == low_cap && low_cap < raw[0] {
        scales[0] = adjust_low_scale_cap(raw[0], low_cap, caps[0]);
    }
    scales[1] = apply_scale_cap(scales[1], linear_scale(maxes[1], caps[1], 3.0), 5);
    scales
}

fn adjust_low_scale_cap(raw: u16, capped: u16, preview_cap: f64) -> u16 {
    let mut adjusted = capped;
    if (94..=98).contains(&capped) {
        adjusted = capped + 2;
    } else if capped == 80 && (0.77..0.82).contains(&preview_cap) {
        adjusted = 82;
    }
    if adjusted > raw {
        return raw;
    }
    adjusted
}

fn scale_for_max(max_value: f64, numerator: f64, round: fn(f64) -> f64) -> u16 {
    if max_value <= 0.0 {
        return 500;
    }
    round(numerator / max_value).clamp(1.0, 500.0) as u16
}

fn linear_scale(max_value: f64, cap_value: f64, max_scale: f64) -> u16 {
    if max_value <= 0.0 {
        return 0;
    }
    let mut scale = 32768.0 / max_value;
    if cap_value > 0.0 && cap_value < scale {
        scale = cap_value;
    }
    if scale > max_scale {
        scale = max_scale;
    }
    if scale < 0.8 {
        scale = 0.8;
    }
    (scale * 100.0).trunc() as u16
}

fn apply_scale_cap(raw: u16, capped: u16, min_delta: u16) -> u16 {
    if raw > capped {
        if raw - capped >= min_delta {
            return capped;
        }
        return raw;
    }
    if capped > raw {
        return capped;
    }
    raw
}

/// The three-band preview before its hold and blend: each lane's average
/// scaled by the preview factors and truncated.
pub(crate) fn preview_from_averages(averages: &[Vec<f64>; 3], norm: &PreviewNorm) -> Vec<u8> {
    let mut out = vec![0u8; COLOR_WIDTH * 3];
    let factors = norm.preview_factors();
    for i in 0..COLOR_WIDTH {
        out[i * 3] = clamp_byte((averages[0][i] * factors[0]).trunc());
        out[i * 3 + 1] = clamp_byte((averages[1][i] * factors[1]).trunc());
        out[i * 3 + 2] = clamp_byte((averages[2][i] * factors[2]).trunc());
    }
    out
}

/// Keeps a decaying share of the previous column where it is louder.
pub(crate) fn preview_hold(entries: &[u8], decay: [f64; 3]) -> Vec<u8> {
    let mut out = entries.to_vec();
    for i in 3..entries.len() {
        let lane = i % 3;
        if decay[lane] <= 0.0 {
            continue;
        }
        let held = (f64::from(entries[i - 3]) * decay[lane]).round() as i64;
        if held > i64::from(out[i]) {
            out[i] = clamp_byte(held as f64);
        }
    }
    out
}

pub(crate) fn preview_blend(entries: &[u8]) -> Vec<u8> {
    blend_preview(entries, [0.45, 0.35, 0.0], [0.05, 0.0, 0.0])
}

fn blend_preview(entries: &[u8], previous: [f64; 3], next: [f64; 3]) -> Vec<u8> {
    let mut out = entries.to_vec();
    let columns = entries.len() / 3;
    for column in 0..columns {
        for lane in 0..3 {
            let prev_weight = previous[lane];
            let next_weight = next[lane];
            if prev_weight <= 0.0 && next_weight <= 0.0 {
                continue;
            }
            let current_weight = 1.0 - prev_weight - next_weight;
            if current_weight < 0.0 {
                continue;
            }
            let current = f64::from(entries[column * 3 + lane]);
            let mut value = current * current_weight;
            if column > 0 {
                value += f64::from(entries[(column - 1) * 3 + lane]) * prev_weight;
            } else {
                value += current * prev_weight;
            }
            if column + 1 < columns {
                value += f64::from(entries[(column + 1) * 3 + lane]) * next_weight;
            } else {
                value += current * next_weight;
            }
            out[column * 3 + lane] = clamp_byte(value.round());
        }
    }
    out
}

/// The per-lane averages behind the three-band preview: `COLOR_WIDTH`
/// columns, each the mean of a run of source columns with a lane-specific
/// lookback window on its first one (the Go's `previewAverages`).
pub(crate) fn preview_averages(source: &[BandColumn]) -> [Vec<f64>; 3] {
    let mut averages = [
        vec![0.0; COLOR_WIDTH],
        vec![0.0; COLOR_WIDTH],
        vec![0.0; COLOR_WIDTH],
    ];
    let n = source.len();
    let windows = [n / 8000, (n / 8000) * 2 / 3, n / 3000];
    let mut source_index = 0usize;
    let mut remainder: i64 = 0;
    let mut column = 0usize;
    while column < COLOR_WIDTH && source_index < n {
        let mut sums = [0.0f64; 3];
        let mut counts = [0usize; 3];
        loop {
            for lane in 0..3 {
                add_preview_source(
                    source,
                    source_index,
                    windows[lane],
                    &mut sums[lane],
                    &mut counts[lane],
                    lane,
                );
            }
            let next = remainder + COLOR_WIDTH as i64;
            source_index += 1;
            if next < n as i64 && source_index < n {
                remainder = next;
                continue;
            }
            remainder = next - n as i64;
            for band in 0..3 {
                if counts[band] > 0 {
                    averages[band][column] = sums[band] / counts[band] as f64;
                }
            }
            break;
        }
        column += 1;
    }
    averages
}

fn add_preview_source(
    source: &[BandColumn],
    source_index: usize,
    window: usize,
    sum: &mut f64,
    count: &mut usize,
    lane: usize,
) {
    let n = source.len();
    let mut start = source_index;
    if *count == 0 {
        if source_index < window {
            *count += window - source_index;
            start = 0;
        } else {
            start = source_index - window;
        }
    }
    if start >= n {
        return;
    }
    let mut end = source_index;
    if end >= n {
        end = n - 1;
    }
    for c in &source[start..=end] {
        *sum += c.lane(lane);
        *count += 1;
    }
}

/// The preview's normalisation: a per-lane balance from the lanes' sums,
/// and the loudest balanced column.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub(crate) struct PreviewNorm {
    balance: [f64; 3],
    max_combined: f64,
}

impl PreviewNorm {
    pub(crate) fn from_averages(averages: &[Vec<f64>; 3]) -> Self {
        let mut sums = [0.0f64; 3];
        for band in 0..3 {
            for &v in &averages[band] {
                sums[band] += v;
            }
        }

        let mut balance = [1.0f64; 3];
        if sums[0] > 0.0 && sums[1] > 0.0 && sums[2] > 0.0 {
            let products = [sums[1] * sums[2], sums[0] * sums[2], sums[0] * sums[1]];
            let scale = 16384.0 / min_positive(&products);
            for i in 0..3 {
                balance[i] = products[i] * scale;
            }
        } else if sums[0] > 0.0 && sums[1] > 0.0 {
            let m = f64::min(sums[0], sums[1]);
            balance[0] = sums[1] * 16384.0 / m;
            balance[1] = sums[0] * 16384.0 / m;
            balance[2] = 16384.0 / m;
        } else if sums[0] > 0.0 && sums[2] > 0.0 {
            let m = f64::min(sums[0], sums[2]);
            balance[0] = sums[2] * 16384.0 / m;
            balance[1] = 1.0;
            balance[2] = sums[0] * 16384.0 / m;
        } else if sums[1] > 0.0 && sums[2] > 0.0 {
            let m = f64::min(sums[1], sums[2]);
            balance[0] = 1.0;
            balance[1] = sums[2] * 16384.0 / m;
            balance[2] = sums[1] * 16384.0 / m;
        }

        let mut max_combined = 0.0f64;
        for i in 0..averages[0].len() {
            let combined = averages[0][i] * balance[0]
                + averages[1][i] * balance[1]
                + averages[2][i] * balance[2];
            max_combined = f64::max(max_combined, combined);
        }
        PreviewNorm {
            balance,
            max_combined,
        }
    }

    fn preview_factors(&self) -> [f64; 3] {
        if self.max_combined <= 0.0 {
            return [0.0; 3];
        }
        [
            preview_pack_factor(self.balance[0] * PREVIEW_LOW_MID_NUMERATOR / self.max_combined),
            preview_pack_factor(self.balance[1] * PREVIEW_LOW_MID_NUMERATOR / self.max_combined),
            preview_pack_factor(self.balance[2] * PREVIEW_HIGH_NUMERATOR / self.max_combined),
        ]
    }

    fn scale_caps(&self) -> [f64; 3] {
        if self.max_combined <= 0.0 {
            return [0.0; 3];
        }
        [
            self.balance[0] * 32768.0 / self.max_combined,
            self.balance[1] * 32768.0 / self.max_combined,
            self.balance[2] * 32768.0 / self.max_combined,
        ]
    }
}

fn preview_pack_factor(v: f64) -> f64 {
    v.clamp(0.8, 5.0) / 256.0
}

fn min_positive(values: &[f64]) -> f64 {
    let mut out = 0.0f64;
    for &v in values {
        if v <= 0.0 {
            continue;
        }
        if out == 0.0 || v < out {
            out = v;
        }
    }
    out
}

/// The three-band detail: `width` columns of (low, mid, high) bytes, each
/// lane a decaying peak follower over the source scaled by its display
/// scale.
pub(crate) fn scroll(source: &[BandColumn], width: usize, scales: [u16; 3]) -> Vec<u8> {
    let mut out = vec![0u8; width * 3];
    if source.is_empty() {
        return out;
    }
    let n = source.len();
    let (mut low_peak, mut mid_peak, mut high_peak) = (0.0f64, 0.0f64, 0.0f64);
    let (mut low_at, mut mid_at, mut high_at) = (0usize, 0usize, 0usize);
    for i in 0..width {
        let mut center = (i + 1) * n / width;
        if center >= n {
            center = n - 1;
        }
        (low_peak, low_at) = peak_follow(source, center, low_at, low_peak, 300, 0.99, 0);
        (mid_peak, mid_at) = peak_follow(source, center, mid_at, mid_peak, 200, 0.98, 1);
        (high_peak, high_at) = peak_follow(source, center, high_at, high_peak, 100, 0.97, 2);

        out[i * 3] = scale_linear_band(low_peak, scales[0]);
        out[i * 3 + 1] = scale_linear_band(mid_peak, scales[1]);
        out[i * 3 + 2] = scale_cosine_band(high_peak, scales[2]);
    }
    out
}

#[allow(clippy::too_many_arguments)]
fn peak_follow(
    source: &[BandColumn],
    center: usize,
    mut pos: usize,
    mut peak: f64,
    lookback: usize,
    decay: f64,
    lane: usize,
) -> (f64, usize) {
    let start = center.saturating_sub(lookback);
    if pos < start || pos > center {
        pos = start;
        peak = 0.0;
    }
    while pos <= center && pos < source.len() {
        let v = source[pos].lane(lane);
        if v > peak {
            peak = v;
        } else {
            peak = v + decay * (peak - v);
        }
        pos += 1;
    }
    (peak, pos)
}

fn scale_linear_band(v: f64, scale: u16) -> u8 {
    clamp_byte((v * f64::from(scale) / 25600.0).trunc())
}

fn scale_cosine_band(v: f64, scale: u16) -> u8 {
    let shaped = 64.0 - 64.0 * (v * std::f64::consts::PI / 32768.0).cos();
    clamp_byte((shaped * f64::from(scale) / 100.0).trunc())
}

pub(crate) fn ceil_div(n: usize, d: usize) -> usize {
    n.div_ceil(d)
}

/// Go's `clampByte`: 0 below zero or NaN, 255 at or above, else truncated.
pub(crate) fn clamp_byte(v: f64) -> u8 {
    if v <= 0.0 || v.is_nan() {
        return 0;
    }
    if v >= 255.0 {
        return 255;
    }
    v as u8
}

/// The three band scales as big-endian u16s: the `three_band_scales` wave.
pub(crate) fn encode_band_scales(scales: [u16; 3]) -> Vec<u8> {
    let mut out = Vec::with_capacity(6);
    for s in scales {
        out.extend_from_slice(&s.to_be_bytes());
    }
    out
}

/// Reduces a byte wave to `target_len` columns by peak.
pub(crate) fn resample_bytes(src: &[u8], target_len: usize) -> Vec<u8> {
    if src.is_empty() {
        return vec![0u8; target_len];
    }
    if src.len() == target_len {
        return src.to_vec();
    }
    let n = src.len();
    let mut out = vec![0u8; target_len];
    for (i, column) in out.iter_mut().enumerate() {
        let start = i * n / target_len;
        let mut end = (i + 1) * n / target_len;
        if end <= start {
            end = start + 1;
        }
        if end > n {
            end = n;
        }
        *column = src[start..end].iter().copied().max().unwrap_or(0);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pack_mono_energy_matches_go() {
        assert_eq!(pack_mono_energy(0.0), 0);
        assert_eq!(pack_mono_energy(0.001), 0x80 | 1);
        assert_eq!(pack_mono_energy(0.5), 0x80 | 16);
        assert_eq!(pack_mono_energy(1.0), 0xc0 | 31);
    }

    #[test]
    fn detail_width_rounds_up() {
        assert_eq!(detail_width(0, 44100), 1);
        assert_eq!(detail_width(44100, 44100), 150);
        assert_eq!(detail_width(44101, 44100), 151);
        assert_eq!(detail_width(147 * 3 + 10, 22050), 4);
    }

    #[test]
    fn source_builder_matches_the_literal_reduction() {
        for &(sr, n) in &[
            (44100u32, 132300usize),
            (22050, 55125),
            (44100, 44133),
            (1000, 2500),
        ] {
            let samples: Vec<i16> = (0..n)
                .map(|i| ((i as f64 / 7.0).sin() * 20000.0) as i16)
                .collect();
            let literal = band_source(&samples, sr);
            let mut filters = Bands::new(sr);
            let mut builder = SourceBuilder::new(sr);
            for &s in &samples {
                builder.push(filters.process(to_f64(s)));
            }
            assert_eq!(builder.finish(), literal, "rate {sr}, {n} samples");
        }
    }
}
