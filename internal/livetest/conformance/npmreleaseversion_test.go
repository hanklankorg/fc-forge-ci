// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

// Read the signed tag, actual published package and Build SBOM independently of
// the builder's success message. This is repeated for both consecutive releases.
func requireNPMVersionContract(t *testing.T, target livetest.Target, repo, tag string) {
	t.Helper()
	version := strings.TrimPrefix(tag, "v")
	mirror := livetest.MirrorRepo(t, target, repo)
	for _, filename := range []string{"package.json", "package-lock.json"} {
		body, err := mirror.Git(t, "show", tag+":"+filename)
		if err != nil {
			t.Fatalf("read signed npm release manifest %s: %v", filename, err)
		}
		var manifest struct {
			Version  string `json:"version"`
			Packages map[string]struct {
				Version string `json:"version"`
			} `json:"packages"`
		}
		if err := json.Unmarshal([]byte(body), &manifest); err != nil || manifest.Version != version {
			t.Fatalf("signed %s does not carry %s: %v", filename, version, err)
		}
		if filename == "package-lock.json" && manifest.Packages[""].Version != version {
			t.Fatalf("signed lockfile root does not carry %s", version)
		}
	}
	packages, sboms := 0, 0
	for _, filename := range livetest.ReleaseAssetNames(t, target, repo, tag) {
		if !strings.HasSuffix(filename, ".tgz") && !strings.HasSuffix(filename, ".cdx.json") {
			continue
		}
		body := livetest.Download(t, target, livetest.ReleaseAssetURL(t, target, repo, tag, filename), maxReleaseFile)
		if strings.HasSuffix(filename, ".cdx.json") {
			var bom struct {
				Metadata struct {
					Component struct {
						Version string `json:"version"`
					} `json:"component"`
				} `json:"metadata"`
			}
			if err := json.Unmarshal(body, &bom); err != nil || bom.Metadata.Component.Version != version {
				t.Fatalf("published Build SBOM disagrees with tag %s: %v", tag, err)
			}
			sboms++
			continue
		}
		stream, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		archive := tar.NewReader(stream)
		found := false
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = stream.Close()
				t.Fatal(err)
			}
			if header.Name != "package/package.json" {
				continue
			}
			if header.Size > 1<<20 {
				_ = stream.Close()
				t.Fatal("published package manifest exceeds bound")
			}
			data, err := io.ReadAll(io.LimitReader(archive, 1<<20))
			var manifest struct {
				Version string `json:"version"`
			}
			if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.Version != version {
				_ = stream.Close()
				t.Fatalf("published tarball does not carry tag version %s", version)
			}
			found = true
			break
		}
		_ = stream.Close()
		if !found {
			t.Fatal("published npm package has no package/package.json")
		}
		packages++
	}
	if packages != 1 || sboms == 0 {
		t.Fatalf("release version proof lacks one package and its Build SBOM: packages=%d sboms=%d", packages, sboms)
	}
	t.Logf("npm %s: signed manifest/lockfile, published tarball and Build SBOM versions agree", version)
}
