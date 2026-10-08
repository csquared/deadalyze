//! The bundle the engine runs in: the interpreter, the libraries, the legs,
//! the checkpoints and the tools, found from the root the engine is started
//! in (`docs/bundle-format.md`). `DEADCA7_BUNDLE` names a root when the
//! engine is run from somewhere else, and `DEADCA7_ALGOS` a directory of
//! legs to use instead of the bundle's (a checkout of `algos/`, for work on
//! a leg against an installed runtime).

use anyhow::{anyhow, Result};
use serde_json::Value;
use std::collections::BTreeMap;
use std::env;
use std::path::{Path, PathBuf};
use std::process::Command;

#[derive(Debug, Clone)]
pub struct Bundle {
    pub root: PathBuf,
    pub algos: PathBuf,
    pub version: Option<String>,
}

impl Bundle {
    /// Resolve: `DEADCA7_BUNDLE`, else the working directory, else the
    /// directory above the executable (`bin/engine` in a bundle).
    pub fn resolve() -> Result<Bundle> {
        let mut candidates = Vec::new();
        if let Ok(p) = env::var("DEADCA7_BUNDLE") {
            candidates.push(PathBuf::from(p));
        }
        if let Ok(cwd) = env::current_dir() {
            candidates.push(cwd);
        }
        if let Ok(exe) = env::current_exe() {
            if let Some(dir) = exe.parent().and_then(Path::parent) {
                candidates.push(dir.to_path_buf());
            }
        }
        for root in candidates {
            if Self::valid(&root) {
                return Ok(Self::at(root));
            }
        }
        Err(anyhow!(
            "no bundle here: run the engine in a bundle root, or set DEADCA7_BUNDLE"
        ))
    }

    pub fn valid(root: &Path) -> bool {
        ["python/bin/python3", "lib", "analysis/lib"]
            .iter()
            .all(|rel| root.join(rel).exists())
    }

    pub fn at(root: PathBuf) -> Bundle {
        let version = std::fs::read_to_string(root.join("manifest.json"))
            .ok()
            .and_then(|s| serde_json::from_str::<Value>(&s).ok())
            .and_then(|m| m.get("version").and_then(Value::as_str).map(String::from));
        let algos = env::var("DEADCA7_ALGOS")
            .map(PathBuf::from)
            .unwrap_or_else(|_| root.join("algos"));
        Bundle {
            root,
            algos,
            version,
        }
    }

    pub fn python(&self) -> PathBuf {
        self.root.join("python/bin/python3")
    }

    /// The bundled interpreter with the bundle activated.
    pub fn py(&self) -> Command {
        let mut c = Command::new(self.python());
        let sep = if cfg!(windows) { ";" } else { ":" };
        c.env(
            "PYTHONPATH",
            format!(
                "{}{sep}{}",
                self.root.join("analysis/lib").display(),
                self.root.join("lib").display()
            ),
        );
        c.env("PYTHONNOUSERSITE", "1");
        c.env("HF_HUB_OFFLINE", "1");
        let path = env::var("PATH").unwrap_or_default();
        c.env(
            "PATH",
            format!("{}{sep}{path}", self.root.join("tools").display()),
        );
        c
    }

    pub fn tool(&self, name: &str) -> PathBuf {
        let bundled = self.root.join("tools").join(name);
        if bundled.exists() {
            return bundled;
        }
        for dir in ["/opt/homebrew/bin", "/usr/local/bin"] {
            let p = Path::new(dir).join(name);
            if p.exists() {
                return p;
            }
        }
        PathBuf::from(name)
    }

    pub fn runner(&self, leg: &str) -> PathBuf {
        self.algos.join(leg).join("runner.py")
    }

    pub fn checkpoint(&self, component: &str, name: &str) -> Option<PathBuf> {
        let p = self.root.join(component).join(name);
        p.exists().then_some(p)
    }

    /// The legs' manifests: name → algo.json.
    pub fn algo_manifests(&self) -> BTreeMap<String, Value> {
        let mut out = BTreeMap::new();
        if let Ok(entries) = std::fs::read_dir(&self.algos) {
            for e in entries.flatten() {
                let m = e.path().join("algo.json");
                if let Ok(s) = std::fs::read_to_string(&m) {
                    if let Ok(v) = serde_json::from_str::<Value>(&s) {
                        out.insert(e.file_name().to_string_lossy().to_string(), v);
                    }
                }
            }
        }
        out
    }

    pub fn ffmpeg_version(&self) -> Option<String> {
        let out = Command::new(self.tool("ffmpeg"))
            .arg("-version")
            .output()
            .ok()?;
        let s = String::from_utf8_lossy(&out.stdout);
        let first = s.lines().next()?;
        // "ffmpeg version 7.1 Copyright ..." or "ffmpeg version N-125450-gfad2e0bc50-https://..."
        let v = first.split_whitespace().nth(2)?.trim_end_matches(',');
        Some(v.split("-https://").next().unwrap_or(v).to_string())
    }

    /// The torch devices the bundle has: cpu always; mps or cuda when torch
    /// reports them. One interpreter start, about a second.
    pub fn devices(&self) -> Vec<String> {
        let code = "import torch;print('mps' if torch.backends.mps.is_available() else '');print('cuda' if torch.cuda.is_available() else '')";
        let mut devices = vec!["cpu".to_string()];
        if let Ok(out) = self.py().args(["-c", code]).output() {
            for line in String::from_utf8_lossy(&out.stdout).lines() {
                let l = line.trim();
                if !l.is_empty() {
                    devices.push(l.to_string());
                }
            }
        }
        devices
    }

    /// `auto` → the best device the bundle has, else the device as asked.
    pub fn pick_device(&self, asked: &str) -> String {
        if asked != "auto" {
            return asked.to_string();
        }
        let devices = self.devices();
        for d in ["cuda", "mps"] {
            if devices.iter().any(|x| x == d) {
                return d.to_string();
            }
        }
        "cpu".to_string()
    }

    pub fn require_leg(&self, leg: &str) -> Result<PathBuf> {
        let r = self.runner(leg);
        if r.exists() {
            Ok(r)
        } else {
            Err(anyhow!("leg {leg} is not in this bundle ({})", r.display()))
        }
    }
}

/// A leg's algo_version from its manifest, else a fallback the engine knows.
pub fn algo_version(manifests: &BTreeMap<String, Value>, leg: &str, fallback: &str) -> String {
    manifests
        .get(leg)
        .and_then(|m| m.get("algo_version"))
        .and_then(Value::as_str)
        .map(String::from)
        .unwrap_or_else(|| fallback.to_string())
}
