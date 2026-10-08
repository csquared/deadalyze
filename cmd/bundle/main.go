// Command bundle handles the published side of an analysis bundle: the
// release manifest the hosts resolve, and checks on a built archive.
//
//	bundle manifest -dist bundle/dist -version ml-v0.2.0 -base https://github.com/csquared/deadalyze/releases/download
//	bundle inspect bundle/dist/deadca7-ml-darwin-arm64.tar.gz     # print the manifest.json inside
//	bundle verify bundle/dist/deadca7-ml-darwin-arm64.tar.gz SHA  # check an archive against a checksum
//
// The release manifest is the shape DEADCA7 and deadcatalog already read from
// deadca7.com/downloads/ml/<tag>/manifest.json: a version and one asset per
// platform (name, sha256, url). docs/bundle-format.md has the whole contract.
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type asset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
}

type manifest struct {
	Version string           `json:"version"`
	Assets  map[string]asset `json:"assets"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bundle:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bundle manifest|inspect|verify ...")
	}
	switch args[0] {
	case "manifest":
		fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
		dist := fs.String("dist", "bundle/dist", "directory with the archives and checksums.txt")
		version := fs.String("version", "", "release tag (ml-vX.Y.Z)")
		base := fs.String("base", "", "URL the assets are served under; the tag is appended")
		out := fs.String("out", "", "write here instead of stdout")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *version == "" {
			return errors.New("-version is required")
		}
		m, err := fromChecksums(*dist, *version, *base)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(m, "", "  ")
		b = append(b, '\n')
		if *out != "" {
			return os.WriteFile(*out, b, 0o644)
		}
		_, err = os.Stdout.Write(b)
		return err
	case "inspect":
		if len(args) < 2 {
			return errors.New("usage: bundle inspect ARCHIVE")
		}
		b, err := manifestInArchive(args[1])
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(b)
		return err
	case "verify":
		if len(args) < 3 {
			return errors.New("usage: bundle verify ARCHIVE SHA256")
		}
		sum, err := fileSHA256(args[1])
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, args[2]) {
			return fmt.Errorf("%s: sha256 %s, want %s", args[1], sum, args[2])
		}
		fmt.Println("ok", sum)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

// fromChecksums turns build.sh's checksums.txt into the release manifest.
// Asset names are deadca7-ml-<goos>-<goarch>.tar.gz; the platform key is
// goos/goarch, as the installers look it up.
func fromChecksums(dist, version, base string) (manifest, error) {
	f, err := os.Open(filepath.Join(dist, "checksums.txt"))
	if err != nil {
		return manifest{}, err
	}
	defer f.Close()
	m := manifest{Version: version, Assets: map[string]asset{}}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		sum, name := fields[0], strings.TrimPrefix(fields[1], "*")
		platform, ok := platformOf(name)
		if !ok {
			continue
		}
		url := name
		if base != "" {
			url = strings.TrimSuffix(base, "/") + "/" + version + "/" + name
		}
		m.Assets[platform] = asset{Name: name, SHA256: sum, URL: url}
	}
	if len(m.Assets) == 0 {
		return manifest{}, fmt.Errorf("no deadca7-ml-*.tar.gz in %s", filepath.Join(dist, "checksums.txt"))
	}
	return m, sc.Err()
}

func platformOf(name string) (string, bool) {
	const prefix, suffix = "deadca7-ml-", ".tar.gz"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	parts := strings.SplitN(strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix), "-", 2)
	if len(parts) != 2 {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

func manifestInArchive(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("no manifest.json in archive")
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == "manifest.json" && strings.Count(strings.Trim(h.Name, "/"), "/") == 1 {
			return io.ReadAll(tr)
		}
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
