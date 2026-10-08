//! DC7F: the grid-independent energy and timbre artifact (recipe 1). The
//! engine checks what the features leg hands back before it publishes it;
//! deadcatalog `analysis/features.Decode`, ported to a validator.

use anyhow::{anyhow, Result};

pub const VERSION: u16 = 1;
pub const HEADER: usize = 64;
pub const FRAME_HZ: u16 = 20;
pub const BANDS: u8 = 3;
pub const MFCCS: u8 = 20;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Header {
    pub frames: u32,
    pub duration_ms: u32,
}

pub fn validate(data: &[u8]) -> Result<Header> {
    if data.len() < HEADER || &data[..4] != b"DC7F" {
        return Err(anyhow!("invalid features header"));
    }
    let u16le = |i: usize| u16::from_le_bytes([data[i], data[i + 1]]);
    let u32le = |i: usize| u32::from_le_bytes([data[i], data[i + 1], data[i + 2], data[i + 3]]);
    let version = u16le(4);
    if version != VERSION {
        return Err(anyhow!("unsupported features version {version}"));
    }
    let frames = u32le(8);
    let duration_ms = u32le(16);
    if u16le(6) != FRAME_HZ
        || data[12] != BANDS
        || data[13] != MFCCS
        || frames == 0
        || duration_ms == 0
    {
        return Err(anyhow!("invalid features dimensions"));
    }
    if data[14..16]
        .iter()
        .chain(data[52..64].iter())
        .any(|b| *b != 0)
    {
        return Err(anyhow!("nonzero features reserved bytes"));
    }
    let expected = HEADER as u64 + frames as u64 * (BANDS as u64 + MFCCS as u64) * 2;
    if data.len() as u64 != expected {
        return Err(anyhow!(
            "features length {}, expected {expected}",
            data.len()
        ));
    }
    for pair in data[HEADER..].chunks_exact(2) {
        let bits = u16::from_le_bytes([pair[0], pair[1]]);
        if bits & 0x7c00 == 0x7c00 {
            return Err(anyhow!("nonfinite features value"));
        }
    }
    Ok(Header {
        frames,
        duration_ms,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn artifact(frames: u32) -> Vec<u8> {
        let mut d = vec![0u8; HEADER];
        d[..4].copy_from_slice(b"DC7F");
        d[4..6].copy_from_slice(&VERSION.to_le_bytes());
        d[6..8].copy_from_slice(&FRAME_HZ.to_le_bytes());
        d[8..12].copy_from_slice(&frames.to_le_bytes());
        d[12] = BANDS;
        d[13] = MFCCS;
        d[16..20].copy_from_slice(&1000u32.to_le_bytes());
        d.resize(HEADER + frames as usize * 23 * 2, 0);
        d
    }

    #[test]
    fn accepts_a_well_formed_artifact() {
        assert_eq!(
            validate(&artifact(20)).unwrap(),
            Header {
                frames: 20,
                duration_ms: 1000
            }
        );
    }

    #[test]
    fn rejects_bad_lengths_and_nan() {
        let mut d = artifact(20);
        d.pop();
        assert!(validate(&d).is_err());
        let mut d = artifact(20);
        d[HEADER] = 0;
        d[HEADER + 1] = 0x7c;
        assert!(validate(&d).is_err());
    }
}
