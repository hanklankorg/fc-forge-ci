// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

const maxAndroidDevelopmentArchive = 256 << 20

func androidDevelopmentAcceptance() developmentAcceptance {
	return developmentAcceptance{
		jobName: "Check: Android development", smokeMarker: "Android development smoke passed at ",
		imageVariable: "CI_ANDROID_JOB_IMAGE", artifactPrefix: "fc-android-dev-",
		maxArchive: maxAndroidDevelopmentArchive, checkArchive: requireAndroidDevelopmentArchive,
	}
}

func TestForgejoLayer_AndroidDevelopment(t *testing.T) {
	runForgejoConsumerDevelopment(t, androidDevelopmentAcceptance())
}

func TestGitLabLayer_AndroidDevelopment(t *testing.T) {
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("set FORGE_CI_LIVE_CONSUMER and FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE for the Android development fixture")
	}
	if livetest.DemoName() != "" {
		t.Fatal("the development trial requires disposable scratch repositories")
	}
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabDevelopment(t, root, visibility.Private, androidDevelopmentAcceptance())
		})
	}
}

// Stream large APK/AAB entries through SHA-256 rather than making another copy.
// The job inspects their internal structures and JUnit results before issuing
// the receipt; acceptance independently binds every downloaded file to it.
func requireAndroidDevelopmentArchive(t *testing.T, body []byte, commit string) {
	t.Helper()
	if len(commit) < 12 || len(body) > maxAndroidDevelopmentArchive {
		t.Fatal("invalid source or oversized Android archive")
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	type fileReceipt struct {
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	var receipt struct {
		Source      string                 `json:"source"`
		Version     string                 `json:"version"`
		VersionCode int                    `json:"versionCode"`
		Files       map[string]fileReceipt `json:"files"`
	}
	seen := map[string]bool{}
	actual := map[string]fileReceipt{}
	var total int64
	var bomVersion string
	for _, file := range archive.File {
		name := strings.TrimPrefix(file.Name, "development-artifacts/")
		if file.FileInfo().IsDir() && file.Name == "development-artifacts/" {
			continue
		}
		if seen[name] || !file.Mode().IsRegular() {
			t.Fatalf("duplicate or non-regular Android artifact %q", file.Name)
		}
		seen[name] = true
		switch name {
		case "app.apk", "app.aab", "bom.json", "output-metadata.json", "receipt.json":
		default:
			t.Fatalf("unexpected Android artifact %q", file.Name)
		}
		limit := int64(maxAndroidDevelopmentArchive) - total
		if name != "app.apk" && name != "app.aab" {
			limit = min(limit, 8<<20)
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		var small bytes.Buffer
		var writer io.Writer = hash
		if name == "receipt.json" || name == "bom.json" {
			writer = io.MultiWriter(hash, &small)
		}
		size, err := io.Copy(writer, io.LimitReader(stream, limit+1))
		_ = stream.Close()
		if err != nil || size > limit || size == 0 {
			t.Fatalf("read bounded Android artifact %s: size=%d error=%v", name, size, err)
		}
		total += size
		if name == "receipt.json" {
			if err := json.Unmarshal(small.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
		} else {
			actual[name] = fileReceipt{SHA256: fmt.Sprintf("%x", hash.Sum(nil)), Size: size}
		}
		if name == "bom.json" {
			bomVersion = buildSBOMVersion(t, small.Bytes())
		}
	}
	version := "0.0.20-dev." + commit[:12]
	if len(seen) != 5 || len(receipt.Files) != 4 || receipt.Source != commit || receipt.Version != version || receipt.VersionCode != 1 || bomVersion != version {
		t.Fatalf("Android receipt does not bind five artifacts to %s at %s: %+v; SBOM=%s", commit, version, receipt, bomVersion)
	}
	for name, got := range actual {
		if receipt.Files[name] != got {
			t.Fatalf("Android artifact %s differs from job receipt: %+v != %+v", name, got, receipt.Files[name])
		}
	}
	t.Logf("Android development archive receipt: source=%s version=%s bytes=%d sha256=%x", commit, version, len(body), sha256.Sum256(body))
}
