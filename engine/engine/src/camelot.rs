//! madmom's key label ("D minor", "Bb major") to Camelot ("7A", "6B").
//! deadcatalog `analysis/key.Camelot` + `catalog.CamelotKey`, ported.

pub fn camelot(label: &str) -> String {
    let Some((note, mode)) = label.trim().split_once(' ') else {
        return String::new();
    };
    let note = note.to_ascii_lowercase();
    match mode.to_ascii_lowercase().as_str() {
        "minor" => camelot_key(&format!("{note}m")),
        "major" => camelot_key(&note),
        _ => String::new(),
    }
}

/// A key in any of the spellings a tag might carry, to Camelot; "" when
/// unknown.
pub fn camelot_key(key: &str) -> String {
    let k = key.trim().to_ascii_lowercase();
    let out = match k.as_str() {
        "abm" | "g#m" | "1a" => "1A",
        "ebm" | "d#m" | "2a" => "2A",
        "bbm" | "a#m" | "3a" => "3A",
        "fm" | "4a" => "4A",
        "cm" | "5a" => "5A",
        "gm" | "6a" => "6A",
        "dm" | "7a" => "7A",
        "am" | "8a" => "8A",
        "em" | "9a" => "9A",
        "bm" | "10a" => "10A",
        "f#m" | "gbm" | "11a" => "11A",
        "c#m" | "dbm" | "12a" => "12A",
        "b" | "1b" => "1B",
        "f#" | "gb" | "2b" => "2B",
        "c#" | "db" | "3b" => "3B",
        "ab" | "g#" | "4b" => "4B",
        "eb" | "d#" | "5b" => "5B",
        "bb" | "a#" | "6b" => "6B",
        "f" | "7b" => "7B",
        "c" | "8b" => "8B",
        "g" | "9b" => "9B",
        "d" | "10b" => "10B",
        "a" | "11b" => "11B",
        "e" | "12b" => "12B",
        _ => "",
    };
    out.to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn labels() {
        assert_eq!(camelot("D minor"), "7A");
        assert_eq!(camelot("Bb major"), "6B");
        assert_eq!(camelot("F# minor"), "11A");
        assert_eq!(camelot("E major"), "12B");
        assert_eq!(camelot(""), "");
        assert_eq!(camelot("H major"), "");
    }
}
