//! The deadca7 engine protocol, v1, as types: what a host writes to
//! `engine analyze` and what the engine answers, one JSON object a line.
//! `docs/engine-protocol.md` is the contract and `protocol/*.schema.json`
//! the same contract as JSON Schema; this crate must agree with both.

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::BTreeMap;

pub const PROTOCOL: u32 = 1;

// ---------------------------------------------------------------- describe

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Leg {
    pub algo_version: String,
    #[serde(rename = "impl")]
    pub impl_: String,
    pub ready: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Decoder {
    pub tool: String,
    pub version: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Limits {
    pub max_batch_items: u32,
    pub max_line_bytes: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SettingSchema {
    #[serde(rename = "type")]
    pub type_: String,
    pub default: Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub unit: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub minimum: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub maximum: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub step: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none", rename = "enum")]
    pub enum_: Option<Vec<Value>>,
    pub group: String,
    pub affects_identity: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Describe {
    pub protocols: Vec<u32>,
    pub engine: EngineIdWire,
    pub recipe: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runtime_version: Option<String>,
    pub tasks: Vec<Task>,
    pub commands: Vec<String>,
    pub legs: BTreeMap<String, Leg>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub decoder: Option<Decoder>,
    pub devices: Vec<String>,
    pub limits: Limits,
    pub settings_schema: BTreeMap<String, SettingSchema>,
}

/// EngineId on the wire: `{"impl": ..., "version": ...}`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EngineIdWire {
    #[serde(rename = "impl")]
    pub impl_: String,
    pub version: String,
}

// ----------------------------------------------------------------- request

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord, Hash)]
#[serde(rename_all = "snake_case")]
pub enum Task {
    Grid,
    Key,
    Features,
    Waveform,
    Cues,
}

impl Task {
    pub fn name(self) -> &'static str {
        match self {
            Task::Grid => "grid",
            Task::Key => "key",
            Task::Features => "features",
            Task::Waveform => "waveform",
            Task::Cues => "cues",
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct Inputs {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub grid: Option<InputGrid>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub features: Option<InputFeatures>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InputGrid {
    pub bpm: f64,
    pub beats_per_bar: u32,
    /// `[beat_number, time_ms]`
    pub beats: Vec<[i64; 2]>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InputFeatures {
    pub format: String,
    pub data: Blob,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Item {
    pub id: String,
    pub audio: String,
    pub tasks: Vec<Task>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cue_algo: Option<String>,
    #[serde(default)]
    pub inputs: Inputs,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Request {
    pub protocol: u32,
    /// A bundle settings document; keys the engine does not know are kept.
    #[serde(default)]
    pub settings: serde_json::Map<String, Value>,
    pub items: Vec<Item>,
}

// ------------------------------------------------------------------ events

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(untagged)]
pub enum Blob {
    B64 { b64: String },
    Path { path: String },
}

impl Blob {
    pub fn b64(bytes: &[u8]) -> Self {
        use base64_impl::encode;
        Blob::B64 { b64: encode(bytes) }
    }
}

/// A dependency-free standard base64, so the protocol crate stays tiny.
mod base64_impl {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    pub fn encode(input: &[u8]) -> String {
        let mut out = String::with_capacity(input.len().div_ceil(3) * 4);
        for chunk in input.chunks(3) {
            let b = [
                chunk[0],
                *chunk.get(1).unwrap_or(&0),
                *chunk.get(2).unwrap_or(&0),
            ];
            let n = (b[0] as u32) << 16 | (b[1] as u32) << 8 | b[2] as u32;
            out.push(TABLE[(n >> 18) as usize & 63] as char);
            out.push(TABLE[(n >> 12) as usize & 63] as char);
            out.push(if chunk.len() > 1 {
                TABLE[(n >> 6) as usize & 63] as char
            } else {
                '='
            });
            out.push(if chunk.len() > 2 {
                TABLE[n as usize & 63] as char
            } else {
                '='
            });
        }
        out
    }
    pub fn decode(input: &str) -> Option<Vec<u8>> {
        let mut out = Vec::with_capacity(input.len() / 4 * 3);
        let mut buf = 0u32;
        let mut bits = 0u32;
        for c in input.bytes() {
            let v = match c {
                b'A'..=b'Z' => c - b'A',
                b'a'..=b'z' => c - b'a' + 26,
                b'0'..=b'9' => c - b'0' + 52,
                b'+' => 62,
                b'/' => 63,
                b'=' | b'\n' | b'\r' => continue,
                _ => return None,
            } as u32;
            buf = buf << 6 | v;
            bits += 6;
            if bits >= 8 {
                bits -= 8;
                out.push((buf >> bits) as u8);
            }
        }
        Some(out)
    }
}

impl Blob {
    /// The bytes of a `b64` blob, or of the file a `path` blob names.
    pub fn bytes(&self) -> std::io::Result<Vec<u8>> {
        match self {
            Blob::B64 { b64 } => base64_impl::decode(b64).ok_or_else(|| {
                std::io::Error::new(std::io::ErrorKind::InvalidData, "invalid base64")
            }),
            Blob::Path { path } => std::fs::read(path),
        }
    }
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Code {
    InvalidRequest,
    UnsupportedProtocol,
    UnsupportedTask,
    AudioMissing,
    DecodeFailed,
    ModelLoadFailed,
    GridFailed,
    LegUnavailable,
    Cancelled,
    EngineCrashed,
    Internal,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum TaskOutcome {
    Ok,
    Failed,
    Skipped,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Verdict {
    Agreed,
    Disputed,
    Unavailable,
    Skipped,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Timeline {
    pub name: String,
    pub offset_ms: f64,
    pub applied_shift_ms: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub decode: Option<TimelineDecode>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TimelineDecode {
    pub flags: Vec<String>,
    pub start_time_s: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct LegIdentity {
    pub algo_version: String,
    pub cfg_hash: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Identity {
    pub recipe: String,
    pub legs: BTreeMap<String, LegIdentity>,
    pub decoder: String,
    pub identity_hash: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RanOn {
    pub device: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runtime_version: Option<String>,
    pub engine: EngineIdWire,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct BeatThisVote {
    pub bpm: f64,
    pub first_beat_ms: Option<i64>,
    pub phase_vote: i64,
    pub phase_agreement: f64,
    pub resid_stdev: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Consensus {
    pub verdict: Verdict,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dispute: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub shift_ms: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub relabel_beats: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub beat_this: Option<BeatThisVote>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Grid {
    pub bpm: f64,
    pub beats_per_bar: u32,
    /// `[beat_number, time_ms]` on the rekordbox timeline.
    pub beats: Vec<[i64; 2]>,
    pub first_beat_ms: i64,
    pub first_downbeat_ms: i64,
    pub timeline: Timeline,
    pub identity: Identity,
    pub ran_on: RanOn,
    pub consensus: Consensus,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub provenance: Option<Value>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct KeyCandidate {
    pub label: String,
    pub p: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Key {
    pub camelot: String,
    pub label: String,
    pub confidence: f64,
    #[serde(default)]
    pub top: Vec<KeyCandidate>,
    pub algo_version: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Cue {
    #[serde(default)]
    pub comment: String,
    pub hot_cue: u32,
    pub time_ms: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Waveform {
    pub kind: String,
    pub entry_bytes: u32,
    pub entry_count: u32,
    pub rate: u32,
    pub source: String,
    pub data: Blob,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub duration_ms: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub band_scales: Option<[u16; 3]>,
}

/// One line of `engine analyze` output. `seq` is added by the writer.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "event", rename_all = "snake_case")]
#[allow(clippy::large_enum_variant)]
pub enum Event {
    BatchStarted {
        engine: EngineIdWire,
        recipe: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        runtime_version: Option<String>,
        settings: Value,
        items: Vec<String>,
    },
    BatchDone {
        items: u32,
        ok: u32,
        failed: u32,
        elapsed_ms: u64,
    },
    BatchError {
        code: Code,
        message: String,
    },
    Progress {
        stage: String,
        done: u32,
        total: u32,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        id: Option<String>,
    },
    ItemStarted {
        id: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        duration_ms: Option<u32>,
    },
    WaveformFrame {
        id: String,
        offset: u32,
        columns: u32,
        columns_per_second: u32,
        data: Blob,
    },
    Waveform {
        id: String,
        #[serde(flatten)]
        wave: Waveform,
    },
    Grid {
        id: String,
        #[serde(flatten)]
        grid: Grid,
    },
    Key {
        id: String,
        #[serde(flatten)]
        key: Key,
    },
    Features {
        id: String,
        format: String,
        algo_version: String,
        data: Blob,
    },
    Cues {
        id: String,
        algo: String,
        cues: Vec<Cue>,
    },
    ItemWarning {
        id: String,
        task: Task,
        code: Code,
        message: String,
    },
    ItemError {
        id: String,
        task: Task,
        code: Code,
        message: String,
        retryable: bool,
    },
    ItemDone {
        id: String,
        tasks: BTreeMap<String, TaskOutcome>,
    },
}

/// An event with its sequence number, as written.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Line {
    pub seq: u64,
    #[serde(flatten)]
    pub event: Event,
}

/// Canonical JSON: sorted keys, no whitespace, shortest round-trip floats,
/// which is what every `cfg_hash` and `identity_hash` is over.
pub fn canonical(value: &Value) -> String {
    fn write(v: &Value, out: &mut String) {
        match v {
            Value::Object(m) => {
                let mut keys: Vec<&String> = m.keys().collect();
                keys.sort();
                out.push('{');
                for (i, k) in keys.iter().enumerate() {
                    if i > 0 {
                        out.push(',');
                    }
                    out.push_str(&serde_json::to_string(k).unwrap());
                    out.push(':');
                    write(&m[*k], out);
                }
                out.push('}');
            }
            Value::Array(a) => {
                out.push('[');
                for (i, x) in a.iter().enumerate() {
                    if i > 0 {
                        out.push(',');
                    }
                    write(x, out);
                }
                out.push(']');
            }
            Value::Number(n) => {
                // Python's repr and Go's 'g' agree with ryu on the digits;
                // Python writes a float with no fraction as "1.0" where
                // serde writes "1.0" too, and 1e-05 where serde writes 1e-5.
                // Identity keys are chosen so neither case arises (see the
                // legs' algo.json); the digits are what matters.
                if let Some(f) = n.as_f64() {
                    if n.is_f64() {
                        out.push_str(&py_float(f));
                        return;
                    }
                }
                out.push_str(&n.to_string());
            }
            _ => out.push_str(&serde_json::to_string(v).unwrap()),
        }
    }
    let mut s = String::new();
    write(value, &mut s);
    s
}

/// Python's `repr(float)`: shortest round-trip, "x.0" for integral values,
/// exponent form below 1e-4 and at or above 1e16, written as Python does
/// (`1e-05`, `1e+16`).
pub fn py_float(f: f64) -> String {
    if f == 0.0 {
        return if f.is_sign_negative() {
            "-0.0".into()
        } else {
            "0.0".into()
        };
    }
    let abs = f.abs();
    if (1e-4..1e16).contains(&abs) {
        let s = format!("{f}");
        if s.contains('.') || s.contains('e') {
            s
        } else {
            format!("{s}.0")
        }
    } else {
        // Shortest digits via Rust's {:e}, then Python's exponent spelling.
        let s = format!("{f:e}");
        let (mant, exp) = s.split_once('e').unwrap();
        let exp: i32 = exp.parse().unwrap();
        format!("{mant}e{}{:02}", if exp < 0 { "-" } else { "+" }, exp.abs())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn canonical_matches_python_dumps() {
        let v = json!({"b": 1, "a": {"z": true, "y": [1.5, 0.005, 4]}, "m": 0.0});
        assert_eq!(
            canonical(&v),
            r#"{"a":{"y":[1.5,0.005,4],"z":true},"b":1,"m":0.0}"#
        );
    }

    #[test]
    fn python_floats() {
        assert_eq!(py_float(0.0), "0.0");
        assert_eq!(py_float(1.0), "1.0");
        assert_eq!(py_float(150.0), "150.0");
        assert_eq!(py_float(0.005), "0.005");
        assert_eq!(py_float(0.00001), "1e-05");
        assert_eq!(py_float(1e16), "1e+16");
    }

    #[test]
    fn base64_round_trip() {
        for n in 0..10 {
            let bytes: Vec<u8> = (0..n).map(|i| (i * 37 % 251) as u8).collect();
            let b = Blob::b64(&bytes);
            assert_eq!(b.bytes().unwrap(), bytes);
        }
        assert_eq!(Blob::b64(b"hi"), Blob::B64 { b64: "aGk=".into() });
    }

    #[test]
    fn event_shape() {
        let line = Line {
            seq: 3,
            event: Event::ItemStarted {
                id: "x".into(),
                duration_ms: Some(1000),
            },
        };
        assert_eq!(
            serde_json::to_string(&line).unwrap(),
            r#"{"seq":3,"event":"item_started","id":"x","duration_ms":1000}"#
        );
    }
}
