//! The band filters: second-order Butterworth biquads (Q = 1/sqrt 2) in direct
//! form 1 on f64, the same structure and arithmetic order as the Go renderer's
//! `biquad`, so the two produce the same samples to within rounding.

use std::f64::consts::PI;

#[derive(Clone, Debug)]
pub(crate) struct Biquad {
    b0: f64,
    b1: f64,
    b2: f64,
    a1: f64,
    a2: f64,
    x1: f64,
    x2: f64,
    y1: f64,
    y2: f64,
}

impl Biquad {
    fn new(b0: f64, b1: f64, b2: f64, a1: f64, a2: f64) -> Self {
        Biquad {
            b0,
            b1,
            b2,
            a1,
            a2,
            x1: 0.0,
            x2: 0.0,
            y1: 0.0,
            y2: 0.0,
        }
    }

    /// A low-pass with its corner at `freq` Hz for a stream at `sr` Hz.
    pub(crate) fn lowpass(freq: f64, sr: u32) -> Self {
        let w0 = 2.0 * PI * freq / f64::from(sr);
        let alpha = w0.sin() / (2.0 * 0.5f64.sqrt());
        let cosw0 = w0.cos();
        let a0 = 1.0 + alpha;
        Biquad::new(
            ((1.0 - cosw0) / 2.0) / a0,
            (1.0 - cosw0) / a0,
            ((1.0 - cosw0) / 2.0) / a0,
            (-2.0 * cosw0) / a0,
            (1.0 - alpha) / a0,
        )
    }

    /// A high-pass with its corner at `freq` Hz for a stream at `sr` Hz.
    pub(crate) fn highpass(freq: f64, sr: u32) -> Self {
        let w0 = 2.0 * PI * freq / f64::from(sr);
        let alpha = w0.sin() / (2.0 * 0.5f64.sqrt());
        let cosw0 = w0.cos();
        let a0 = 1.0 + alpha;
        Biquad::new(
            ((1.0 + cosw0) / 2.0) / a0,
            (-(1.0 + cosw0)) / a0,
            ((1.0 + cosw0) / 2.0) / a0,
            (-2.0 * cosw0) / a0,
            (1.0 - alpha) / a0,
        )
    }

    #[inline]
    pub(crate) fn process(&mut self, x: f64) -> f64 {
        let y = self.b0 * x + self.b1 * self.x1 + self.b2 * self.x2
            - self.a1 * self.y1
            - self.a2 * self.y2;
        self.x2 = self.x1;
        self.x1 = x;
        self.y2 = self.y1;
        self.y1 = y;
        y
    }
}

/// The five filters behind the three bands: low is a 300 Hz low-pass, mid a
/// 250 Hz high-pass into a 1200 Hz low-pass, high a 3000 Hz high-pass into a
/// 9000 Hz low-pass.
#[derive(Clone, Debug)]
pub(crate) struct Bands {
    low: Biquad,
    mid_high: Biquad,
    mid_low: Biquad,
    high_high: Biquad,
    high_low: Biquad,
}

impl Bands {
    pub(crate) fn new(sr: u32) -> Self {
        Bands {
            low: Biquad::lowpass(300.0, sr),
            mid_high: Biquad::highpass(250.0, sr),
            mid_low: Biquad::lowpass(1200.0, sr),
            high_high: Biquad::highpass(3000.0, sr),
            high_low: Biquad::lowpass(9000.0, sr),
        }
    }

    /// The magnitude of each band for the next sample (0..1 of full scale).
    #[inline]
    pub(crate) fn process(&mut self, sample: f64) -> [f64; 3] {
        [
            self.low.process(sample).abs(),
            self.mid_low.process(self.mid_high.process(sample)).abs(),
            self.high_low.process(self.high_high.process(sample)).abs(),
        ]
    }
}
