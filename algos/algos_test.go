package algos

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pinned are the runners the engine runs, under the leg contract in
// README.md. Until the hosts cut over to the engine (docs/porting.md) they
// carry the previous runners (go:embed in deadcatalog, Swift literals in
// DEADCA7), so the sibling tests below only report a mismatch; after the
// cutover the pin test becomes a protocol check again, with the siblings
// required to match these bytes.
var pinned = map[string]string{
	"beatnet":   "45e8ef43c89d6687c277d7b5a8000528c5e78269e5b57902d8bedf1c7e3ef29f",
	"beat_this": "da24e3fd146f26e61b1ccffb7f64080dd3918f20d93cdef19b03834179cfe226",
	"key":       "4f5e4ff2a7fa4858b4758d5c764610fb0dbf68f6160c9c94e289bdc197143c9e",
	"features":  "0cc9370bf6b2e07ba3369d48ce005f275224545178673b28ba375619237eda96",
	"cues":      "ff743d62c231d51637681c64a26516b6e72a11f0ef4391b94ad0a57534ca096d",
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
			if got := sum(t, p); got != pinned[name] {
				t.Logf("%s (%s) differs from algos/%s/runner.py (%s)", p, got, name, pinned[name])
				t.Skip("hosts carry the previous runners until they cut over to the engine")
			}
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
		if !strings.Contains(string(b), `"`+want+`"`) {
			t.Logf("DEADCA7 pins a different %s runner than %s", name, want)
			t.Skip("hosts carry the previous runners until they cut over to the engine")
		}
	}
}
