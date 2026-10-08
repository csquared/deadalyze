package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManifestFromChecksums(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(
		"aaaa  deadca7-ml-darwin-arm64.tar.gz\nbbbb  deadca7-ml-linux-amd64.tar.gz\ncccc  checksums.txt\n"), 0o644))
	m, err := fromChecksums(dir, "ml-v0.2.0", "https://example.com/releases/download/")
	require.NoError(t, err)
	require.Equal(t, "ml-v0.2.0", m.Version)
	require.Len(t, m.Assets, 2)
	require.Equal(t, "aaaa", m.Assets["darwin/arm64"].SHA256)
	require.Equal(t, "https://example.com/releases/download/ml-v0.2.0/deadca7-ml-linux-amd64.tar.gz", m.Assets["linux/amd64"].URL)
}
