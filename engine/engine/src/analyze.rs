//! The batch: BeatNet over every item, Beat This over every item,
//! arbitration, the key pass, with the waveforms and the features drawn
//! beside the model passes, and each item's events out as soon as they
//! are made (`docs/engine-protocol.md`).

use crate::bundle::{algo_version, Bundle};
use crate::consensus::{self, RECIPE};
use crate::legs;
use crate::out::Out;
use crate::settings::Settings;
use crate::{camelot, dc7f, timeline};
use protocol::{
    Blob, Code, Consensus, Cue, EngineIdWire, Event, Grid, Identity, Item, Key, KeyCandidate,
    RanOn, Request, Task, TaskOutcome, Timeline, TimelineDecode, Verdict, Waveform,
};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Condvar, Mutex};
use std::time::Instant;

pub const ENGINE_IMPL: &str = "deadca7-engine";
pub const ENGINE_VERSION: &str = env!("CARGO_PKG_VERSION");

pub fn engine_id() -> EngineIdWire {
    EngineIdWire {
        impl_: ENGINE_IMPL.into(),
        version: ENGINE_VERSION.into(),
    }
}

/// Per-item bookkeeping shared across the threads of a batch.
#[derive(Default)]
struct State {
    outcomes: HashMap<String, BTreeMap<String, TaskOutcome>>,
    /// Items whose waveform task is finished (ok or failed), so a grid
    /// never goes out before the waves.
    waves_done: HashSet<String>,
    features: HashMap<String, Vec<u8>>,
}

struct Shared {
    state: Mutex<State>,
    waves: Condvar,
    out: Out,
}

impl Shared {
    fn set(&self, id: &str, task: Task, outcome: TaskOutcome) {
        let mut s = self.state.lock().unwrap();
        s.outcomes
            .entry(id.to_string())
            .or_default()
            .insert(task.name().to_string(), outcome);
    }
    fn wave_done(&self, id: &str) {
        let mut s = self.state.lock().unwrap();
        s.waves_done.insert(id.to_string());
        self.waves.notify_all();
    }
    fn wait_wave(&self, id: &str) {
        let mut s = self.state.lock().unwrap();
        while !s.waves_done.contains(id) {
            s = self.waves.wait(s).unwrap();
        }
    }
}

pub fn run(bundle: &Bundle, req: &Request, out: Out) -> (u32, u32) {
    let started = Instant::now();
    let ids: Vec<String> = req.items.iter().map(|i| i.id.clone()).collect();
    let mut settings = Settings::from_document(&req.settings);
    let needs_model = req
        .items
        .iter()
        .any(|i| i.tasks.iter().any(|t| matches!(t, Task::Grid | Task::Key)));
    let device = if needs_model {
        bundle.pick_device(&settings.device)
    } else {
        settings.device.clone()
    };
    settings = settings.with_device(&device);
    out.emit(Event::BatchStarted {
        engine: engine_id(),
        recipe: RECIPE.into(),
        runtime_version: bundle.version.clone(),
        settings: Value::Object(settings.resolved.clone()),
        items: ids.clone(),
    });
    let shared = Arc::new(Shared {
        state: Mutex::new(State::default()),
        waves: Condvar::new(),
        out,
    });
    let ffprobe = bundle.tool("ffprobe");
    let ffmpeg = bundle.tool("ffmpeg");
    let manifests = bundle.algo_manifests();

    // Which items can do what: a missing file fails every task but a cues
    // task fed entirely from inputs.
    let mut runnable: HashMap<String, Vec<Task>> = HashMap::new();
    for item in &req.items {
        let o = &shared.out;
        o.emit(Event::ItemStarted {
            id: item.id.clone(),
            duration_ms: timeline::duration_ms(&ffprobe, Path::new(&item.audio)),
        });
        let exists = Path::new(&item.audio).is_file();
        let mut tasks = Vec::new();
        for t in &item.tasks {
            let from_inputs =
                *t == Task::Cues && item.inputs.grid.is_some() && item.inputs.features.is_some();
            if exists || from_inputs {
                tasks.push(*t);
            } else {
                o.error(
                    &item.id,
                    *t,
                    Code::AudioMissing,
                    format!("no file at {}", item.audio),
                    false,
                );
                shared.set(&item.id, *t, TaskOutcome::Failed);
            }
        }
        runnable.insert(item.id.clone(), tasks);
    }
    let wants = |item: &Item, t: Task| {
        runnable
            .get(&item.id)
            .map(|v| v.contains(&t))
            .unwrap_or(false)
    };

    // Waveforms on their own thread, one item after another, frames out
    // as they decode. Every item is marked done even without the task so
    // the grid never waits on nothing.
    let wave_items: Vec<(String, String)> = req
        .items
        .iter()
        .filter(|i| wants(i, Task::Waveform))
        .map(|i| (i.id.clone(), i.audio.clone()))
        .collect();
    for item in &req.items {
        if !wants(item, Task::Waveform) {
            shared.wave_done(&item.id);
        }
    }
    let wave_thread = {
        let shared = shared.clone();
        let ffmpeg = ffmpeg.clone();
        std::thread::spawn(move || {
            let total = wave_items.len() as u32;
            for (n, (id, audio)) in wave_items.iter().enumerate() {
                if crate::procguard::cancelled() {
                    shared.wave_done(id);
                    continue;
                }
                let path = PathBuf::from(audio);
                let flags = timeline::decoder_flags(&path);
                let flag_refs: Vec<&str> = flags.iter().map(String::as_str).collect();
                let o = &shared.out;
                let mut on_frame = |f: wave::Frame| o.frame(id, &f);
                match wave::render_file(&ffmpeg, &flag_refs, &path, &mut on_frame) {
                    Ok(r) => {
                        for (i, w) in r.waves.iter().enumerate() {
                            o.emit(Event::Waveform {
                                id: id.clone(),
                                wave: Waveform {
                                    kind: w.kind.to_string(),
                                    entry_bytes: w.entry_bytes,
                                    entry_count: w.entry_count,
                                    rate: w.rate,
                                    source: w.source.to_string(),
                                    data: Blob::b64(&w.data),
                                    duration_ms: (i == 0).then_some(r.duration_ms),
                                    band_scales: (i == 0).then_some(r.band_scales),
                                },
                            });
                        }
                        shared.set(id, Task::Waveform, TaskOutcome::Ok);
                    }
                    Err(e) => {
                        o.error(id, Task::Waveform, Code::DecodeFailed, e.to_string(), false);
                        shared.set(id, Task::Waveform, TaskOutcome::Failed);
                    }
                }
                shared.wave_done(id);
                o.progress("waveform", n as u32 + 1, total);
            }
        })
    };

    // Features on their own thread: one leg batch.
    let feature_items: Vec<(String, String)> = req
        .items
        .iter()
        .filter(|i| wants(i, Task::Features))
        .map(|i| (i.id.clone(), i.audio.clone()))
        .collect();
    let features_version = algo_version(&manifests, "features", "dc7f-1");
    let feature_thread = {
        let shared = shared.clone();
        let bundle = bundle.clone();
        let version = features_version.clone();
        std::thread::spawn(move || {
            if feature_items.is_empty() {
                return;
            }
            let o = &shared.out;
            let res = legs::run_batch(&bundle, "features", &[], &feature_items, |id, r| match r {
                Ok(v) => {
                    let data = v
                        .get("data")
                        .and_then(|d| serde_json::from_value::<Blob>(d.clone()).ok())
                        .and_then(|b| b.bytes().ok());
                    match data
                        .ok_or_else(|| "no data".to_string())
                        .and_then(|b| dc7f::validate(&b).map(|_| b).map_err(|e| e.to_string()))
                    {
                        Ok(bytes) => {
                            o.emit(Event::Features {
                                id: id.to_string(),
                                format: "dc7f".into(),
                                algo_version: v
                                    .get("algo_version")
                                    .and_then(Value::as_str)
                                    .unwrap_or(&version)
                                    .to_string(),
                                data: Blob::b64(&bytes),
                            });
                            shared
                                .state
                                .lock()
                                .unwrap()
                                .features
                                .insert(id.to_string(), bytes);
                            shared.set(id, Task::Features, TaskOutcome::Ok);
                        }
                        Err(e) => {
                            o.error(id, Task::Features, Code::Internal, e, true);
                            shared.set(id, Task::Features, TaskOutcome::Failed);
                        }
                    }
                }
                Err(e) => {
                    o.error(id, Task::Features, Code::LegUnavailable, e, true);
                    shared.set(id, Task::Features, TaskOutcome::Failed);
                }
            });
            if let Err(e) = res {
                eprintln!("engine: features: {e}");
            }
            o.progress(
                "features",
                feature_items.len() as u32,
                feature_items.len() as u32,
            );
        })
    };

    // Keys on their own thread, beside the grid legs: the model loaded
    // once, over the items that asked. madmom runs on the CPU while the
    // grid legs hold the device, so a single track pays for one of them,
    // and the key lands before the grid does.
    let key_items: Vec<(String, String)> = req
        .items
        .iter()
        .filter(|i| wants(i, Task::Key))
        .map(|i| (i.id.clone(), i.audio.clone()))
        .collect();
    let key_version = algo_version(&manifests, "key", "madmom-key-cnn-2018");
    let key_thread = {
        let shared = shared.clone();
        let bundle = bundle.clone();
        let device = device.clone();
        let version = key_version;
        std::thread::spawn(move || {
            if key_items.is_empty() || crate::procguard::cancelled() {
                return;
            }
            let o = &shared.out;
            let total = key_items.len() as u32;
            let mut n = 0;
            let res = legs::run_batch(
                &bundle,
                "key",
                &["--device".to_string(), device],
                &key_items,
                |id, r| {
                    n += 1;
                    let key = match r {
                        Ok(v) => {
                            let label = v
                                .get("label")
                                .and_then(Value::as_str)
                                .unwrap_or("")
                                .to_string();
                            let camelot = camelot::camelot(&label);
                            Key {
                                camelot: camelot.clone(),
                                label,
                                confidence: v
                                    .get("confidence")
                                    .and_then(Value::as_f64)
                                    .unwrap_or(0.0),
                                top: v
                                    .get("top")
                                    .and_then(Value::as_array)
                                    .map(|a| {
                                        a.iter()
                                            .map(|c| KeyCandidate {
                                                label: c
                                                    .get("label")
                                                    .and_then(Value::as_str)
                                                    .unwrap_or("")
                                                    .into(),
                                                p: c.get("p")
                                                    .and_then(Value::as_f64)
                                                    .unwrap_or(0.0),
                                            })
                                            .collect()
                                    })
                                    .unwrap_or_default(),
                                algo_version: v
                                    .get("algo_version")
                                    .and_then(Value::as_str)
                                    .unwrap_or(&version)
                                    .to_string(),
                                note: camelot.is_empty().then(|| "no recognized key".to_string()),
                            }
                        }
                        Err(e) => {
                            o.warning(id, Task::Key, Code::LegUnavailable, e.clone());
                            Key {
                                camelot: "".into(),
                                label: "".into(),
                                confidence: 0.0,
                                top: vec![],
                                algo_version: version.clone(),
                                note: Some(format!("unavailable: {e}")),
                            }
                        }
                    };
                    o.emit(Event::Key {
                        id: id.to_string(),
                        key,
                    });
                    shared.set(id, Task::Key, TaskOutcome::Ok);
                    o.progress("key", n, total);
                },
            );
            if let Err(e) = res {
                eprintln!("engine: key: {e}");
            }
        })
    };

    // The grid legs, on this thread.
    let grid_items: Vec<(String, String)> = req
        .items
        .iter()
        .filter(|i| wants(i, Task::Grid))
        .map(|i| (i.id.clone(), i.audio.clone()))
        .collect();
    let mut grids: HashMap<String, Grid> = HashMap::new();
    if !grid_items.is_empty() && !crate::procguard::cancelled() {
        let o = &shared.out;
        let beatnet_version = algo_version(&manifests, "beatnet", "beatnet-dbn-v15");
        let beat_this_version = algo_version(&manifests, "beat_this", "beat-this-v2");
        let mut common = vec![
            "--beats-per-bar".to_string(),
            settings.beats_per_bar.to_string(),
            "--device".to_string(),
            device.clone(),
        ];
        if !settings.origin_refine {
            common.push("--no-refine".into());
        }
        let mut bn_args = vec![
            "--min-bpm".to_string(),
            "70".to_string(),
            "--max-bpm".to_string(),
            "150".to_string(),
            "--model".to_string(),
            "1".to_string(),
            "--origin-frame-snap".to_string(),
            "0".to_string(),
        ];
        bn_args.extend(common.iter().cloned());
        let mut ours: HashMap<String, legs::GridLeg> = HashMap::new();
        let total = grid_items.len() as u32;
        let res = legs::run_batch(bundle, "beatnet", &bn_args, &grid_items, |id, r| {
            match r.map_err(|e| e.to_string()).and_then(|v| {
                legs::check_cfg_hash("beatnet", &v);
                legs::parse_grid("beatnet", &v, &beatnet_version).map_err(|e| e.to_string())
            }) {
                Ok(g) => {
                    ours.insert(id.to_string(), g);
                }
                Err(e) => {
                    let code = if e.contains("No such file") || e.contains("ModuleNotFoundError") {
                        Code::ModelLoadFailed
                    } else {
                        Code::GridFailed
                    };
                    o.error(id, Task::Grid, code, e, true);
                    shared.set(id, Task::Grid, TaskOutcome::Failed);
                }
            }
            o.progress("beatnet", ours.len() as u32, total);
        });
        if let Err(e) = res {
            eprintln!("engine: beatnet: {e}");
        }

        // The cross-check over the items that have a grid.
        let mut theirs: HashMap<String, legs::GridLeg> = HashMap::new();
        let mut unavailable: HashMap<String, String> = HashMap::new();
        if settings.consensus && !ours.is_empty() && !crate::procguard::cancelled() {
            let model = bundle
                .checkpoint("beatthis", "final0.ckpt")
                .map(|p| p.to_string_lossy().to_string())
                .unwrap_or_else(|| "final0".into());
            let mut bt_args = vec![
                "--model".to_string(),
                model,
                "--origin-frame-snap".to_string(),
                "0".to_string(),
            ];
            bt_args.extend(common.iter().cloned());
            let bt_items: Vec<(String, String)> = grid_items
                .iter()
                .filter(|(id, _)| ours.contains_key(id))
                .cloned()
                .collect();
            let total = bt_items.len() as u32;
            let mut n = 0;
            let res = legs::run_batch(bundle, "beat_this", &bt_args, &bt_items, |id, r| {
                n += 1;
                match r.map_err(|e| e.to_string()).and_then(|v| {
                    legs::check_cfg_hash("beat_this", &v);
                    legs::parse_grid("beat_this", &v, &beat_this_version).map_err(|e| e.to_string())
                }) {
                    Ok(g) => {
                        theirs.insert(id.to_string(), g);
                    }
                    Err(e) => {
                        unavailable.insert(id.to_string(), e);
                    }
                }
                o.progress("beat_this", n, total);
            });
            if let Err(e) = res {
                eprintln!("engine: beat_this: {e}");
            }
        }

        // Arbitration, the timeline, and the grid events, each after its
        // item's waves.
        let decoder = format!(
            "ffmpeg-{}",
            bundle.ffmpeg_version().unwrap_or_else(|| "unknown".into())
        );
        for (id, audio) in &grid_items {
            let Some(mut leg) = ours.remove(id) else {
                continue;
            };
            let mut legs_id = BTreeMap::new();
            legs_id.insert("beatnet".to_string(), leg.identity.clone());
            let consensus = if !settings.consensus {
                Consensus {
                    verdict: Verdict::Skipped,
                    dispute: None,
                    shift_ms: None,
                    relabel_beats: None,
                    beat_this: None,
                    note: None,
                }
            } else if let Some(other) = theirs.get(id) {
                legs_id.insert("beat_this".to_string(), other.identity.clone());
                consensus::arbitrate(&mut leg.grid, &other.grid, other.resid_stdev, &settings)
                    .consensus
            } else {
                let why = unavailable
                    .get(id)
                    .cloned()
                    .unwrap_or_else(|| "beat_this returned nothing".into());
                o.warning(id, Task::Grid, Code::LegUnavailable, why.clone());
                Consensus {
                    verdict: Verdict::Unavailable,
                    dispute: None,
                    shift_ms: None,
                    relabel_beats: None,
                    beat_this: None,
                    note: Some(why),
                }
            };
            let path = Path::new(audio);
            let start_s = match timeline::start_time_s(&ffprobe, path) {
                Ok(s) => s,
                Err(e) => {
                    o.warning(
                        id,
                        Task::Grid,
                        Code::DecodeFailed,
                        format!("{e}; times left on the trimmed decode"),
                    );
                    0.0
                }
            };
            let offset_ms = start_s * 1000.0;
            let shift = offset_ms.round() as i64;
            let g = &leg.grid;
            let beats: Vec<[i64; 2]> = g.beats.iter().map(|(n, t)| [*n, t + shift]).collect();
            let first_beat_ms = beats.first().map(|b| b[1]).unwrap_or(0);
            let first_downbeat_ms = beats
                .iter()
                .find(|b| b[0] == 1)
                .map(|b| b[1])
                .unwrap_or(first_beat_ms);
            let identity_doc = json!({"recipe": RECIPE, "legs": legs_id, "decoder": decoder});
            let identity_hash = format!(
                "{:x}",
                Sha256::digest(protocol::canonical(&identity_doc).as_bytes())
            );
            let grid = Grid {
                bpm: g.bpm,
                beats_per_bar: g.beats_per_bar,
                beats,
                first_beat_ms,
                first_downbeat_ms,
                timeline: Timeline {
                    name: timeline::REKORDBOX.into(),
                    offset_ms,
                    applied_shift_ms: shift,
                    decode: Some(TimelineDecode {
                        flags: timeline::decoder_flags(path),
                        start_time_s: start_s,
                    }),
                },
                identity: Identity {
                    recipe: RECIPE.into(),
                    legs: legs_id,
                    decoder: decoder.clone(),
                    identity_hash,
                },
                ran_on: RanOn {
                    device: device.clone(),
                    runtime_version: bundle.version.clone(),
                    engine: engine_id(),
                },
                consensus,
                provenance: Some(
                    json!({"beatnet": leg.provenance, "origin_refine_ms": leg.origin_refine_ms}),
                ),
            };
            shared.wait_wave(id);
            o.emit(Event::Grid {
                id: id.clone(),
                grid: grid.clone(),
            });
            shared.set(id, Task::Grid, TaskOutcome::Ok);
            grids.insert(id.clone(), grid);
        }
    }

    let _ = key_thread.join();
    let _ = wave_thread.join();
    let _ = feature_thread.join();

    // Cues: from this batch's grid and features, or the host's.
    for item in &req.items {
        if !wants(item, Task::Cues) {
            continue;
        }
        let o = &shared.out;
        let grid_in = grids
            .get(&item.id)
            .map(|g| (g.bpm, g.beats_per_bar, g.beats.clone()))
            .or_else(|| {
                item.inputs
                    .grid
                    .as_ref()
                    .map(|g| (g.bpm, g.beats_per_bar, g.beats.clone()))
            });
        let features_in = shared
            .state
            .lock()
            .unwrap()
            .features
            .get(&item.id)
            .cloned()
            .or_else(|| {
                item.inputs
                    .features
                    .as_ref()
                    .and_then(|f| f.data.bytes().ok())
            });
        let (Some((_bpm, _bar, beats)), Some(features)) = (grid_in, features_in) else {
            o.error(
                &item.id,
                Task::Cues,
                Code::InvalidRequest,
                "cues need a grid and features, from this request's tasks or its inputs",
                false,
            );
            shared.set(&item.id, Task::Cues, TaskOutcome::Failed);
            continue;
        };
        if crate::procguard::cancelled() {
            break;
        }
        match pick_cues(
            bundle,
            &item.audio,
            item.cue_algo.as_deref().unwrap_or("mix16"),
            &beats,
            &features,
        ) {
            Ok((algo, cues)) => {
                o.emit(Event::Cues {
                    id: item.id.clone(),
                    algo,
                    cues,
                });
                shared.set(&item.id, Task::Cues, TaskOutcome::Ok);
            }
            Err(e) => {
                o.error(
                    &item.id,
                    Task::Cues,
                    Code::LegUnavailable,
                    e.to_string(),
                    true,
                );
                shared.set(&item.id, Task::Cues, TaskOutcome::Failed);
            }
        }
    }

    // Done, item by item, then the batch.
    let cancelled = crate::procguard::cancelled();
    let mut ok = 0;
    let mut failed = 0;
    let o = &shared.out;
    for item in &req.items {
        let mut tasks = shared
            .state
            .lock()
            .unwrap()
            .outcomes
            .remove(&item.id)
            .unwrap_or_default();
        for t in &item.tasks {
            if !tasks.contains_key(t.name()) {
                if cancelled {
                    o.error(&item.id, *t, Code::Cancelled, "cancelled", true);
                    tasks.insert(t.name().to_string(), TaskOutcome::Failed);
                } else {
                    tasks.insert(t.name().to_string(), TaskOutcome::Skipped);
                }
            }
        }
        if tasks.values().any(|v| *v == TaskOutcome::Failed) {
            failed += 1;
        } else {
            ok += 1;
        }
        o.emit(Event::ItemDone {
            id: item.id.clone(),
            tasks,
        });
    }
    o.emit(Event::BatchDone {
        items: req.items.len() as u32,
        ok,
        failed,
        elapsed_ms: started.elapsed().as_millis() as u64,
    });
    (ok, failed)
}

/// The cues leg on a grid and a DC7F artifact, no audio read.
fn pick_cues(
    bundle: &Bundle,
    audio: &str,
    algo: &str,
    beats: &[[i64; 2]],
    features: &[u8],
) -> anyhow::Result<(String, Vec<Cue>)> {
    let dir = std::env::temp_dir().join(format!("deadca7-engine-{}", std::process::id()));
    std::fs::create_dir_all(&dir)?;
    let path = dir.join(format!(
        "{}.dc7f",
        Sha256::digest(audio.as_bytes())
            .iter()
            .take(8)
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    ));
    std::fs::write(&path, features)?;
    let secs: Vec<f64> = beats.iter().map(|b| b[1] as f64 / 1000.0).collect();
    let downs: Vec<f64> = beats
        .iter()
        .filter(|b| b[0] == 1)
        .map(|b| b[1] as f64 / 1000.0)
        .collect();
    let args = vec![
        "--features".to_string(),
        path.to_string_lossy().to_string(),
        "--beats".to_string(),
        serde_json::to_string(&secs)?,
        "--downbeats".to_string(),
        serde_json::to_string(&downs)?,
        "--algo".to_string(),
        algo.to_string(),
    ];
    let v = legs::run_single(bundle, "cues", &args);
    let _ = std::fs::remove_file(&path);
    let v = v?;
    let cues = v
        .get("cues")
        .and_then(Value::as_array)
        .map(|a| {
            a.iter()
                .map(|c| Cue {
                    comment: c
                        .get("comment")
                        .and_then(Value::as_str)
                        .unwrap_or("")
                        .into(),
                    hot_cue: c.get("hot_cue").and_then(Value::as_u64).unwrap_or(0) as u32,
                    time_ms: c.get("time_ms").and_then(Value::as_i64).unwrap_or(0),
                })
                .collect()
        })
        .unwrap_or_default();
    Ok((
        v.get("algo")
            .and_then(Value::as_str)
            .unwrap_or(algo)
            .to_string(),
        cues,
    ))
}
