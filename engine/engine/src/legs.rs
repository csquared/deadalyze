//! Running a Python leg: one interpreter over a batch of items, the model
//! loaded once, one JSON line back per item (`algos/README.md`). Every
//! child is registered with the process guard so a cancel reaches it.

use crate::bundle::Bundle;
use crate::consensus::LegGrid;
use crate::procguard;
use anyhow::{anyhow, Result};
use protocol::LegIdentity;
use serde_json::Value;
use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read};
use std::process::{Command, Stdio};

/// The outcome for one item of a batch.
pub type LegResult = std::result::Result<Value, String>;

/// Run `leg` over `items` (id, audio) with `args` after the input, and
/// hand each item's line to `on_item` as it lands. Items the runner never
/// answered get the exit status and the stderr tail. The Err is for a
/// runner that answered nothing at all.
pub fn run_batch(
    bundle: &Bundle,
    leg: &str,
    args: &[String],
    items: &[(String, String)],
    mut on_item: impl FnMut(&str, LegResult),
) -> Result<()> {
    if items.is_empty() {
        return Ok(());
    }
    let runner = bundle.require_leg(leg)?;
    let list: Vec<Value> = items
        .iter()
        .map(|(id, audio)| serde_json::json!({"id": id, "audio": audio}))
        .collect();
    let mut cmd = bundle.py();
    cmd.arg(&runner)
        .arg("--ffmpeg")
        .arg(bundle.tool("ffmpeg"))
        .arg("--items")
        .arg(serde_json::to_string(&list)?);
    cmd.args(args);
    run(
        cmd,
        leg,
        items.iter().map(|(id, _)| id.as_str()),
        &mut on_item,
    )
}

/// Run `leg` once on one input (no `--items`): a runner without a batch
/// mode, such as cues. The single JSON object on stdout is the answer.
pub fn run_single(bundle: &Bundle, leg: &str, args: &[String]) -> Result<Value> {
    let runner = bundle.require_leg(leg)?;
    let mut cmd = bundle.py();
    cmd.arg(&runner).arg("--ffmpeg").arg(bundle.tool("ffmpeg"));
    cmd.args(args);
    cmd.stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let child = cmd.spawn().map_err(|e| anyhow!("{leg}: {e}"))?;
    let pid = child.id();
    procguard::register(pid);
    let out = child.wait_with_output();
    procguard::unregister(pid);
    let out = out?;
    if !out.status.success() {
        return Err(anyhow!(
            "{leg}: {}: {}",
            out.status,
            tail(&String::from_utf8_lossy(&out.stderr))
        ));
    }
    serde_json::from_slice(&out.stdout).map_err(|e| {
        anyhow!(
            "decode {leg} output: {e}: {}",
            String::from_utf8_lossy(&out.stdout).trim()
        )
    })
}

fn run<'a>(
    mut cmd: Command,
    leg: &str,
    ids: impl Iterator<Item = &'a str>,
    on_item: &mut impl FnMut(&str, LegResult),
) -> Result<()> {
    cmd.stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = cmd.spawn().map_err(|e| anyhow!("{leg}: {e}"))?;
    let pid = child.id();
    procguard::register(pid);
    let stdout = child.stdout.take().unwrap();
    let mut stderr = child.stderr.take().unwrap();
    let debug = std::env::var("DEADCA7_ENGINE_LOG")
        .map(|v| v == "debug")
        .unwrap_or(false);
    let leg_name = leg.to_string();
    let err_thread = std::thread::spawn(move || {
        let mut buf = String::new();
        if debug {
            let mut reader = BufReader::new(stderr);
            let mut line = String::new();
            while reader.read_line(&mut line).map(|n| n > 0).unwrap_or(false) {
                eprint!("[{leg_name}] {line}");
                buf.push_str(&line);
                line.clear();
            }
        } else {
            let _ = stderr.read_to_string(&mut buf);
        }
        buf
    });
    let mut seen: HashMap<String, bool> = HashMap::new();
    let reader = BufReader::with_capacity(1 << 20, stdout);
    for line in reader.split(b'\n') {
        let Ok(line) = line else { break };
        if line.iter().all(u8::is_ascii_whitespace) {
            continue;
        }
        let Ok(v) = serde_json::from_slice::<Value>(&line) else {
            continue;
        };
        let Some(id) = v.get("id").and_then(Value::as_str).map(String::from) else {
            continue;
        };
        seen.insert(id.clone(), true);
        match v.get("error").and_then(Value::as_str) {
            Some(e) if !e.is_empty() => on_item(&id, Err(e.to_string())),
            _ => on_item(&id, Ok(v)),
        }
    }
    let status = child.wait();
    procguard::unregister(pid);
    let stderr_text = err_thread.join().unwrap_or_default();
    let status = status?;
    let failure = if status.success() {
        None
    } else {
        Some(format!("{leg} batch: {status}: {}", tail(&stderr_text)))
    };
    for id in ids {
        if !seen.contains_key(id) {
            on_item(
                id,
                Err(failure
                    .clone()
                    .unwrap_or_else(|| format!("{leg} batch returned nothing for this track"))),
            );
        }
    }
    if let (Some(f), true) = (failure, seen.is_empty()) {
        return Err(anyhow!(f));
    }
    Ok(())
}

/// The last lines of a leg's stderr: what a person needs from a traceback.
pub fn tail(s: &str) -> String {
    let lines: Vec<&str> = s.trim().lines().collect();
    let n = lines.len();
    lines[n.saturating_sub(6)..].join("\n")
}

// ------------------------------------------------------------ the parsers

pub struct GridLeg {
    pub grid: LegGrid,
    pub identity: LegIdentity,
    pub origin_refine_ms: f64,
    pub resid_stdev: f64,
    pub provenance: Value,
}

/// A grid runner's result (beatnet or beat_this) as the recipe sees it.
pub fn parse_grid(leg: &str, v: &Value, fallback_version: &str) -> Result<GridLeg> {
    let grid = v.get("grid").ok_or_else(|| anyhow!("{leg}: no grid"))?;
    let bpm = grid.get("bpm").and_then(Value::as_f64).unwrap_or(0.0);
    let beats_per_bar = grid
        .get("beats_per_bar")
        .and_then(Value::as_u64)
        .unwrap_or(4) as u32;
    let beats: Vec<(i64, i64)> = v
        .get("beats")
        .and_then(Value::as_array)
        .map(|a| {
            a.iter()
                .map(|b| {
                    (
                        b.get("beat_number").and_then(Value::as_i64).unwrap_or(0),
                        b.get("time_ms").and_then(Value::as_i64).unwrap_or(0),
                    )
                })
                .collect()
        })
        .unwrap_or_default();
    if beats.is_empty() || bpm <= 0.0 {
        return Err(anyhow!("{leg}: empty grid"));
    }
    let mut lg = LegGrid {
        bpm,
        beats_per_bar,
        beats,
        first_beat_ms: grid.get("first_beat_ms").and_then(Value::as_i64),
        first_downbeat_ms: grid.get("first_downbeat_ms").and_then(Value::as_i64),
        phase_vote: v.get("bt_phase_vote").and_then(Value::as_i64).unwrap_or(0),
        phase_agreement: v
            .get("bt_phase_agreement")
            .and_then(Value::as_f64)
            .unwrap_or(0.0),
    };
    if lg.first_beat_ms.is_none() {
        lg.first_beat_ms = Some(lg.beats[0].1);
    }
    if lg.first_downbeat_ms.is_none() {
        lg.first_downbeat_ms = lg.beats.iter().find(|b| b.0 == 1).map(|b| b.1);
    }
    let identity = LegIdentity {
        algo_version: v
            .get("algo_version")
            .and_then(Value::as_str)
            .unwrap_or(fallback_version)
            .to_string(),
        cfg_hash: v
            .get("cfg_hash")
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_string(),
    };
    let mut provenance = serde_json::Map::new();
    for k in [
        "config",
        "anchor",
        "raw_bpm",
        "raw_beats",
        "bpm_digits",
        "dynamic",
        "duration",
        "identity",
    ] {
        if let Some(x) = v.get(k) {
            provenance.insert(k.into(), x.clone());
        }
    }
    Ok(GridLeg {
        grid: lg,
        identity,
        origin_refine_ms: v
            .get("origin_refine_ms")
            .and_then(Value::as_f64)
            .unwrap_or(0.0),
        resid_stdev: v
            .get("bt_resid_stdev")
            .and_then(Value::as_f64)
            .unwrap_or(0.0),
        provenance: Value::Object(provenance),
    })
}

/// Verify a leg's `cfg_hash` against its reported identity config, when it
/// reports one; a mismatch is a bug in a leg and is worth a line on stderr.
pub fn check_cfg_hash(leg: &str, v: &Value) {
    let Some(cfg) = v.get("identity").and_then(|i| i.get("cfg")) else {
        return;
    };
    let Some(reported) = v.get("cfg_hash").and_then(Value::as_str) else {
        return;
    };
    use sha2::{Digest, Sha256};
    let canon = protocol::canonical(cfg);
    let ours = format!("{:x}", Sha256::digest(canon.as_bytes()));
    if ours != reported {
        eprintln!("engine: {leg} cfg_hash {reported} but canonical {canon} hashes to {ours}");
    }
}
