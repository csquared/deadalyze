//! rekordbox's colour waveforms, from deadcatalog's `analysis/wavecolor`:
//! PWV4 (the 1200-column, six-lane preview) and PWV5 (the colour detail, a
//! big-endian u16 a column: red, green and blue in three bits each at 15-13,
//! 12-10 and 9-7, the mono detail's height in five at 6-2), both derived
//! from the three-band detail wave.

/// Columns in the colour preview (PWV4).
pub const PREVIEW_WIDTH: usize = 1200;

/// PWV4: six bytes a column for `PREVIEW_WIDTH` columns, from the detail
/// wave's per-lane peaks.
pub fn preview(detail: &[u8]) -> Vec<u8> {
    from_bands(&resample(detail, PREVIEW_WIDTH))
}

/// PWV5: one big-endian u16 a detail column. `mono` is the mono detail,
/// whose height (its low five bits) PWV5 repeats; `scales` are the display
/// scales the detail was packed with, undone so the colour sees the band
/// energy rather than the track's scaling.
pub fn detail(detail: &[u8], mono: &[u8], scales: [u16; 3]) -> Vec<u8> {
    let n = detail.len() / 3;
    let mut out = vec![0u8; n * 2];
    let mut unscale = [0.0f64; 3];
    for (lane, &s) in scales.iter().enumerate() {
        let s = if s == 0 { 100 } else { s };
        unscale[lane] = 100.0 / f64::from(s);
    }
    for i in 0..n {
        let low = f64::from(detail[i * 3]) * unscale[0];
        let mid = f64::from(detail[i * 3 + 1]) * unscale[1];
        let high = f64::from(detail[i * 3 + 2]) * unscale[2];
        let sum = low + mid + high + 1.0;
        let features = [low, mid, high, 1.0, low / sum, mid / sum, high / sum];
        let mut rgb = [0u16; 3];
        for (c, weights) in DETAIL_WEIGHTS.iter().enumerate() {
            let mut v = 0.0f64;
            for (k, f) in features.iter().enumerate() {
                v += f * weights[k];
            }
            rgb[c] = v.round().clamp(0.0, 7.0) as u16;
        }
        let height = if i < mono.len() {
            u16::from(mono[i] & 0x1f)
        } else {
            0
        };
        let packed = rgb[0] << 13 | rgb[1] << 10 | rgb[2] << 7 | height << 2;
        out[i * 2..i * 2 + 2].copy_from_slice(&packed.to_be_bytes());
    }
    out
}

/// Maps (low, mid, high, 1, low/sum, mid/sum, high/sum) of the unscaled
/// bands to rekordbox's three-bit red, green and blue.
const DETAIL_WEIGHTS: [[f64; 7]; 3] = [
    [
        0.0188281, 0.0289312, 0.0434023, 3.72048, 4.99576, -6.62306, -10.6358,
    ],
    [
        -0.0413379, 0.060421, 0.0513837, 4.86252, -2.29026, 1.16652, -6.63715,
    ],
    [
        -0.0208309,
        -0.00797648,
        0.0217414,
        6.66695,
        -3.11942,
        -1.51518,
        16.3072,
    ],
];

/// Reduces a three-band wave to `width` columns, keeping each lane's peak.
pub(crate) fn resample(wave: &[u8], width: usize) -> Vec<u8> {
    let cols = wave.len() / 3;
    if cols == 0 || width == 0 {
        return Vec::new();
    }
    let mut out = vec![0u8; width * 3];
    for i in 0..width {
        let start = i * cols / width;
        let mut end = (i + 1) * cols / width;
        if end <= start {
            end = start + 1;
        }
        if end > cols {
            end = cols;
        }
        for band in 0..3 {
            let mut peak = 0u8;
            for src in start..end {
                let v = wave[src * 3 + band];
                if v > peak {
                    peak = v;
                }
            }
            out[i * 3 + band] = peak;
        }
    }
    out
}

/// Maps low/mid/high u7 columns to PWV4's six lanes (deadca7's
/// colorFrom3Band): the lanes are not RGB plus padding, every one varies in
/// rekordbox's exports, and the coefficients are fit to one.
pub(crate) fn from_bands(wave: &[u8]) -> Vec<u8> {
    let cols = wave.len() / 3;
    let clamp = |v: f64| v.round().clamp(0.0, 255.0) as u8;
    let clamp127 = |v: f64| v.round().clamp(0.0, 127.0) as u8;
    let mut b = vec![0u8; cols * 6];
    for i in 0..cols {
        let low = f64::from(wave[i * 3]);
        let mid = f64::from(wave[i * 3 + 1]);
        let high = f64::from(wave[i * 3 + 2]);
        b[i * 6] = clamp127(0.451 * low + 0.356 * mid + 0.495 * high + 24.055);
        b[i * 6 + 1] = clamp(-0.456 * low - 0.386 * mid - 0.462 * high + 228.221);
        b[i * 6 + 2] = clamp127(0.995 * low + 0.174 * mid - 0.026 * high - 3.113);
        b[i * 6 + 3] = clamp127(1.115 * low - 0.029 * mid - 0.031 * high - 7.992);
        b[i * 6 + 4] = clamp127(0.014 * low + 0.587 * mid - 0.073 * high - 2.593);
        b[i * 6 + 5] = clamp127(0.065 * low + 0.014 * mid + 0.387 * high + 6.843);
    }
    b
}

#[cfg(test)]
mod tests {
    use super::*;

    // deadca7's overview golden: twelve detail columns reduced to four by
    // lane peaks, then the six-lane fit.
    #[test]
    fn preview_matches_deadca7_golden() {
        let detail: [u8; 36] = [
            12, 18, 4, 80, 70, 20, 20, 30, 8, 110, 100, 40, 8, 12, 4, 30, 36, 12, 0, 0, 0, 127,
            127, 127, 20, 5, 90, 60, 40, 10, 90, 20, 5, 15, 80, 12,
        ];
        let peaks = resample(&detail, 4);
        assert_eq!(peaks, [80, 70, 20, 110, 100, 40, 127, 127, 127, 90, 80, 12]);
        assert_eq!(
            from_bands(&peaks),
            [
                95, 155, 88, 79, 38, 21, 127, 121, 123, 111, 55, 31, 127, 63, 127, 126, 64, 66, 99,
                151, 100, 90, 45, 18
            ]
        );
        assert_eq!(preview(&detail).len(), PREVIEW_WIDTH * 6);
    }

    // PWV5 is rekordbox's layout: red, green, blue in three bits (15-13,
    // 12-10, 9-7), the mono detail's height in five (6-2), nothing in 1-0.
    #[test]
    fn detail_layout() {
        let bands = [0, 0, 0, 120, 10, 2, 5, 100, 4, 10, 20, 120, 127, 127, 127];
        let mono = [0, 0xe5, 0x3f, 0x11, 0xff];
        let got = detail(&bands, &mono, [100, 100, 100]);
        assert_eq!(got.len(), 10);
        let mut entries = Vec::new();
        for (i, pair) in got.chunks(2).enumerate() {
            let v = u16::from_be_bytes([pair[0], pair[1]]);
            assert_eq!(v & 3, 0, "bits 1-0 of entry {i}");
            entries.push((v >> 13, v >> 10 & 7, v >> 7 & 7, v >> 2 & 31));
            assert_eq!(u16::from(mono[i] & 0x1f), entries[i].3, "entry {i} height");
        }
        assert_eq!(
            entries,
            [
                (4, 5, 7, 0),
                (7, 0, 1, 5),
                (1, 7, 5, 31),
                (1, 7, 7, 17),
                (7, 7, 7, 31)
            ]
        );
    }

    // The colour sees the band energy, not the track's display scaling.
    #[test]
    fn detail_undoes_the_display_scales() {
        let mono = [10u8];
        let plain = detail(&[60, 30, 10], &mono, [100, 100, 100]);
        let doubled = detail(&[120, 60, 20], &mono, [200, 200, 200]);
        assert_eq!(plain, doubled);
    }
}
