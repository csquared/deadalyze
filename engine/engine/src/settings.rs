//! The settings document a request carries (`settings/README.md`), with the
//! defaults from `settings/dance4x4.json` and the schema `describe` shows.

use protocol::SettingSchema;
use serde_json::{json, Map, Value};
use std::collections::BTreeMap;

#[derive(Debug, Clone)]
pub struct Settings {
    pub min_bpm: f64,
    pub max_bpm: f64,
    pub beats_per_bar: u32,
    pub origin_refine: bool,
    pub consensus: bool,
    pub arbitrate: bool,
    pub arbitrate_min_vote_agreement: f64,
    pub arbitrate_half_beat_slop_ms: f64,
    pub consensus_max_bpm_delta: f64,
    pub consensus_max_origin_ms: f64,
    pub consensus_max_phase_beats: f64,
    pub consensus_min_vote_agreement: f64,
    pub device: String,
    /// The document as given plus the defaults filled in: what
    /// `batch_started` reports.
    pub resolved: Map<String, Value>,
}

impl Default for Settings {
    fn default() -> Self {
        Settings::from_document(&Map::new())
    }
}

impl Settings {
    pub fn from_document(doc: &Map<String, Value>) -> Settings {
        let f = |k: &str, d: f64| doc.get(k).and_then(Value::as_f64).unwrap_or(d);
        let b = |k: &str, d: bool| doc.get(k).and_then(Value::as_bool).unwrap_or(d);
        let mut s = Settings {
            min_bpm: f("min_bpm", 110.0),
            max_bpm: f("max_bpm", 130.0),
            beats_per_bar: doc
                .get("beats_per_bar")
                .and_then(Value::as_u64)
                .unwrap_or(4) as u32,
            origin_refine: b("origin_refine", true),
            consensus: b("consensus", true),
            arbitrate: b("arbitrate", true),
            arbitrate_min_vote_agreement: f("arbitrate_min_vote_agreement", 0.7),
            arbitrate_half_beat_slop_ms: f("arbitrate_half_beat_slop_ms", 60.0),
            consensus_max_bpm_delta: f("consensus_max_bpm_delta", 0.05),
            consensus_max_origin_ms: f("consensus_max_origin_ms", 60.0),
            consensus_max_phase_beats: f("consensus_max_phase_beats", 0.5),
            consensus_min_vote_agreement: f("consensus_min_vote_agreement", 0.6),
            device: doc
                .get("device")
                .and_then(Value::as_str)
                .unwrap_or("auto")
                .to_string(),
            resolved: doc.clone(),
        };
        if s.beats_per_bar == 0 || s.beats_per_bar > 16 {
            s.beats_per_bar = 4;
        }
        for (k, v) in [
            ("min_bpm", json!(s.min_bpm)),
            ("max_bpm", json!(s.max_bpm)),
            ("beats_per_bar", json!(s.beats_per_bar)),
            ("origin_refine", json!(s.origin_refine)),
            ("consensus", json!(s.consensus)),
            ("arbitrate", json!(s.arbitrate)),
            (
                "arbitrate_min_vote_agreement",
                json!(s.arbitrate_min_vote_agreement),
            ),
            (
                "arbitrate_half_beat_slop_ms",
                json!(s.arbitrate_half_beat_slop_ms),
            ),
            ("consensus_max_bpm_delta", json!(s.consensus_max_bpm_delta)),
            ("consensus_max_origin_ms", json!(s.consensus_max_origin_ms)),
            (
                "consensus_max_phase_beats",
                json!(s.consensus_max_phase_beats),
            ),
            (
                "consensus_min_vote_agreement",
                json!(s.consensus_min_vote_agreement),
            ),
            ("device", json!(s.device)),
        ] {
            s.resolved.entry(k.to_string()).or_insert(v);
        }
        s
    }

    /// The device as it will be reported once `auto` is resolved.
    pub fn with_device(mut self, device: &str) -> Settings {
        self.device = device.to_string();
        self.resolved.insert("device".into(), json!(device));
        self
    }
}

fn entry(type_: &str, default: Value, title: &str, group: &str, identity: bool) -> SettingSchema {
    SettingSchema {
        type_: type_.into(),
        default,
        title: Some(title.into()),
        unit: None,
        minimum: None,
        maximum: None,
        step: None,
        enum_: None,
        group: group.into(),
        affects_identity: identity,
    }
}

/// What `describe.settings_schema` lists.
pub fn schema(devices: &[String]) -> BTreeMap<String, SettingSchema> {
    let mut m = BTreeMap::new();
    let mut put = |k: &str, e: SettingSchema| {
        m.insert(k.to_string(), e);
    };
    let mut e = entry(
        "number",
        json!(110),
        "Lowest tempo the primary leg anchors to",
        "grid",
        true,
    );
    e.minimum = Some(40.0);
    e.maximum = Some(300.0);
    e.unit = Some("BPM".into());
    put("min_bpm", e);
    let mut e = entry(
        "number",
        json!(130),
        "Highest tempo the primary leg anchors to",
        "grid",
        true,
    );
    e.minimum = Some(40.0);
    e.maximum = Some(300.0);
    e.unit = Some("BPM".into());
    put("max_bpm", e);
    let mut e = entry("integer", json!(4), "Beats per bar", "grid", true);
    e.minimum = Some(1.0);
    e.maximum = Some(16.0);
    put("beats_per_bar", e);
    put(
        "origin_refine",
        entry(
            "boolean",
            json!(true),
            "Polish the grid's origin against the audio",
            "grid",
            true,
        ),
    );
    put(
        "consensus",
        entry(
            "boolean",
            json!(true),
            "Cross-check the grid with Beat This",
            "recipe",
            true,
        ),
    );
    put(
        "arbitrate",
        entry(
            "boolean",
            json!(true),
            "Apply the cross-checker's fixes (off: only flag disputes)",
            "recipe",
            true,
        ),
    );
    let mut e = entry(
        "number",
        json!(0.7),
        "Relabel bar 1 on the cross-checker's vote at this agreement",
        "recipe",
        true,
    );
    e.minimum = Some(0.5);
    e.maximum = Some(1.0);
    e.step = Some(0.05);
    put("arbitrate_min_vote_agreement", e);
    let mut e = entry(
        "number",
        json!(60),
        "Half-beat lock tolerance",
        "recipe",
        true,
    );
    e.unit = Some("ms".into());
    put("arbitrate_half_beat_slop_ms", e);
    let mut e = entry(
        "number",
        json!(0.05),
        "Tempo disagreement that makes a dispute",
        "recipe",
        true,
    );
    e.unit = Some("BPM".into());
    put("consensus_max_bpm_delta", e);
    let mut e = entry(
        "number",
        json!(60),
        "Origin disagreement that makes a dispute",
        "recipe",
        true,
    );
    e.unit = Some("ms".into());
    put("consensus_max_origin_ms", e);
    let mut e = entry(
        "number",
        json!(0.5),
        "Downbeat disagreement that makes a dispute",
        "recipe",
        true,
    );
    e.unit = Some("beats".into());
    put("consensus_max_phase_beats", e);
    let mut e = entry(
        "number",
        json!(0.6),
        "Report the vote as a dispute at this agreement",
        "recipe",
        true,
    );
    e.minimum = Some(0.0);
    e.maximum = Some(1.0);
    put("consensus_min_vote_agreement", e);
    let mut e = entry(
        "string",
        json!("auto"),
        "Device the model legs run on",
        "runtime",
        false,
    );
    let mut options = vec![json!("auto")];
    options.extend(devices.iter().map(|d| json!(d)));
    e.enum_ = Some(options);
    put("device", e);
    m
}
