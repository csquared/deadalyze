package algos

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// pinned are the runners the hosts embed. deadcatalog's go:embed copies and
// DEADCA7's Swift literals hash to the same values (RunnerScriptTests pins
// them there); a change here is a change to every host.
var pinned = map[string]string{
	"beatnet":   "c3e09d9cbef26d4f8ecd0badcaa7e1f98854a87054e06d341078e04245f8e349",
	"beat_this": "da59474cb7508adb09508e41372d13c1ad04d9dc0c6cc11344223fd3e0a0da8d",
	"key":       "7365846033a816b79a8f700eb2cc686c02f4efa23bb132be554f67e8dbd12662",
	"features":  "b0927c76071b040b6c286c98a4b5a46711ad1463922903b1485e7fda10a166cb",
	"cues":      "d5e38a0a1496775fe059cc11be1c34f0636eb62501574f73c56ddfe625069ce1",
}

// siblings are where the hosts keep their copies, relative to this repo.
var siblings = map[string][]string{
	"beatnet":   {"../../deadcatalog/analysis/internal/beatnet/runner.py"},
	"beat_this": {"../../deadcatalog/analysis/internal/beat_this/runner.py"},
	"key":       {"../../deadcatalog/analysis/key/runner.py"},
	"features":  {"../../deadcatalog/analysis/features/runner.py"},
	"cues":      {"../../deadcatalog/analysis/cues/runner.py"},
}

func sum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestRunnersArePinned(t *testing.T) {
	for name, want := range pinned {
		require.Equal(t, want, sum(t, filepath.Join(name, "runner.py")), name)
	}
}

func TestEveryAlgoHasAManifest(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	seen := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		seen++
		b, err := os.ReadFile(filepath.Join(e.Name(), "algo.json"))
		require.NoError(t, err, e.Name())
		var m struct {
			Name, Kind, AlgoVersion, Runner string `json:",omitempty"`
		}
		var raw map[string]any
		require.NoError(t, json.Unmarshal(b, &raw), e.Name())
		for _, key := range []string{"name", "kind", "algo_version", "runner", "flags", "requires"} {
			require.Contains(t, raw, key, "%s/algo.json", e.Name())
		}
		require.Equal(t, e.Name(), raw["name"], "algo.json name must match its directory")
		require.Contains(t, []any{"grid", "key", "features", "cues"}, raw["kind"], e.Name())
		_ = m
		require.FileExists(t, filepath.Join(e.Name(), raw["runner"].(string)))
	}
	require.Equal(t, len(pinned), seen, "every algorithm directory is pinned")
}

// TestSiblingsAgree checks the hosts' embedded copies against these, when the
// sibling checkouts are beside this repo. It is how a runner edit is caught
// before it ships in one host and not the other.
func TestSiblingsAgree(t *testing.T) {
	for name, paths := range siblings {
		for _, p := range paths {
			if _, err := os.Stat(p); err != nil {
				t.Skipf("sibling %s not checked out", p)
			}
			require.Equal(t, pinned[name], sum(t, p), "%s drifted from algos/%s/runner.py", p, name)
		}
	}
}

// TestSwiftLiteralsAgree reads DEADCA7's RunnerScripts.swift and checks the
// pins it carries in AnalysisCoreTests match ours.
func TestSwiftLiteralsAgree(t *testing.T) {
	path := "../../deadca7/apple/Tests/EngineTests/AnalysisCoreTests.swift"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skip("deadca7 not checked out beside this repo")
	}
	for name, want := range pinned {
		require.Contains(t, string(b), `"`+want+`"`, "DEADCA7 pins a different %s runner", name)
	}
}
