//! The deadca7 analysis engine: one executable behind docs/engine-protocol.md.
//!
//!     engine describe
//!     engine analyze < request.json
//!
//! Run in a bundle root (or with DEADCA7_BUNDLE naming one). The model legs
//! are the Python runners in the bundle's `algos/` (or DEADCA7_ALGOS) until
//! each is native; the recipe, the waveforms, the timeline and the key
//! mapping are native here.

mod analyze;
mod bundle;
mod camelot;
mod consensus;
mod dc7f;
mod legs;
mod out;
mod procguard;
mod settings;
mod timeline;

use anyhow::Result;
use protocol::{Code, Decoder, Describe, Event, Leg, Limits, Request, Task, PROTOCOL};
use std::collections::BTreeMap;
use std::io::Read;

const MAX_BATCH_ITEMS: u32 = 64;
const MAX_LINE_BYTES: u64 = 16 * 1024 * 1024;

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let code = match args.first().map(String::as_str) {
        Some("describe") => describe(),
        Some("analyze") => analyze(),
        Some("version") => {
            println!("{} {}", analyze::ENGINE_IMPL, analyze::ENGINE_VERSION);
            Ok(0)
        }
        _ => {
            eprintln!("usage: engine describe | analyze < request.json");
            Ok(2)
        }
    };
    match code {
        Ok(c) => std::process::exit(c),
        Err(e) => {
            eprintln!("engine: {e:#}");
            std::process::exit(1)
        }
    }
}

fn describe() -> Result<i32> {
    let bundle = bundle::Bundle::resolve()?;
    let manifests = bundle.algo_manifests();
    let devices = bundle.devices();
    let mut legs = BTreeMap::new();
    let python_leg = |name: &str, fallback: &str, ready: bool, note: Option<String>| Leg {
        algo_version: bundle::algo_version(&manifests, name, fallback),
        impl_: "python".into(),
        ready,
        note,
    };
    let has = |leg: &str| bundle.runner(leg).exists();
    legs.insert(
        "beatnet".into(),
        python_leg("beatnet", "beatnet-dbn-v15", has("beatnet"), None),
    );
    let ckpt = bundle.checkpoint("beatthis", "final0.ckpt").is_some();
    legs.insert(
        "beat_this".into(),
        python_leg(
            "beat_this",
            "beat-this-v2",
            has("beat_this") && ckpt,
            (!ckpt).then(|| "beatthis/final0.ckpt missing".to_string()),
        ),
    );
    legs.insert(
        "key".into(),
        python_leg("key", "madmom-key-cnn-2018", has("key"), None),
    );
    legs.insert(
        "features".into(),
        python_leg("features", "dc7f-1", has("features"), None),
    );
    legs.insert(
        "cues".into(),
        python_leg("cues", "mix16", has("cues"), None),
    );
    legs.insert(
        "waveform".into(),
        Leg {
            algo_version: "wave-v1".into(),
            impl_: "native".into(),
            ready: true,
            note: None,
        },
    );
    let d = Describe {
        protocols: vec![PROTOCOL],
        engine: analyze::engine_id(),
        recipe: consensus::RECIPE.into(),
        runtime_version: bundle.version.clone(),
        tasks: vec![
            Task::Grid,
            Task::Key,
            Task::Features,
            Task::Waveform,
            Task::Cues,
        ],
        commands: vec!["describe".into(), "analyze".into()],
        legs,
        decoder: bundle.ffmpeg_version().map(|v| Decoder {
            tool: "ffmpeg".into(),
            version: v,
        }),
        devices: devices.clone(),
        limits: Limits {
            max_batch_items: MAX_BATCH_ITEMS,
            max_line_bytes: MAX_LINE_BYTES,
        },
        settings_schema: settings::schema(&devices),
    };
    println!("{}", serde_json::to_string_pretty(&d)?);
    Ok(0)
}

fn analyze() -> Result<i32> {
    procguard::init();
    let out = out::Out::stdout();
    let mut raw = String::new();
    std::io::stdin().read_to_string(&mut raw)?;
    let req: Request = match serde_json::from_str(&raw) {
        Ok(r) => r,
        Err(e) => {
            out.emit(Event::BatchError {
                code: Code::InvalidRequest,
                message: format!("request: {e}"),
            });
            return Ok(2);
        }
    };
    if req.protocol != PROTOCOL {
        out.emit(Event::BatchError {
            code: Code::UnsupportedProtocol,
            message: format!("protocol {} (this engine speaks {PROTOCOL})", req.protocol),
        });
        return Ok(2);
    }
    if req.items.is_empty() || req.items.len() as u32 > MAX_BATCH_ITEMS {
        out.emit(Event::BatchError {
            code: Code::InvalidRequest,
            message: format!("{} items (1..{MAX_BATCH_ITEMS})", req.items.len()),
        });
        return Ok(2);
    }
    let mut ids = std::collections::HashSet::new();
    for item in &req.items {
        if item.id.is_empty() || !ids.insert(&item.id) || item.tasks.is_empty() {
            out.emit(Event::BatchError {
                code: Code::InvalidRequest,
                message: format!(
                    "item {:?}: ids must be unique and non-empty, tasks non-empty",
                    item.id
                ),
            });
            return Ok(2);
        }
    }
    let bundle = match bundle::Bundle::resolve() {
        Ok(b) => b,
        Err(e) => {
            out.emit(Event::BatchError {
                code: Code::Internal,
                message: e.to_string(),
            });
            return Ok(2);
        }
    };
    let (_ok, _failed) = analyze::run(&bundle, &req, out);
    Ok(0)
}
