// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// This is registry/runner acceptance, not an Android SDK build claim. A native
// JAR and an AAR-shaped archive exercise both publication layouts; real Gradle
// producer behavior belongs to the companion toolchain tests.
func TestInRunner_ExportedMavenPublications(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	for _, forge := range livetest.ForgesMeeting(t, forgesClaiming(t, alwaysValidatesTokens, "a Maven registry"), livetest.NeedsInRunner, livetest.NeedsPackages) {
		for _, mode := range []string{"release", "snapshot"} {
			t.Run(string(forge)+"/"+mode, func(t *testing.T) {
				t.Parallel()
				target := livetest.Accept(t, forge)
				repo := livetest.NewScratchRepo(t, target, "mavenfiles-"+mode)

				version := forgePackageVersion(time.Now())
				if mode == "snapshot" {
					version += "-SNAPSHOT"
				}

				const group = "com.example.livetest"
				for _, kind := range []string{"jar", "aar"} {
					name := livetest.MavenPackageName(target, group, "fci-export-"+kind)
					if slices.Contains(livetest.PublishedPackageVersions(t, target, repo, "maven", name), version) {
						t.Fatal("fixture version already exists")
					}

					t.Cleanup(func() { livetest.DeletePublishedPackage(t, target, repo, "maven", name, version) })
				}

				const tag = "v0.0.10-mavenfiles"
				livetest.PrepareTag(t, target, repo, tag)
				livetest.PublishBinaryAsset(t, target, repo, tag, t.TempDir())

				if forge == provider.ForgeForgejo {
					livetest.SetRepoSecret(t, target, repo, packageTokenSecret, target.Token)
				}

				address := livetest.ReleaseAssetURL(t, target, repo, tag, "forge-ci")
				if conclusion := livetest.RunWorkflow(t, target, repo, "maven-publications", exportedPublicationProbe(target.Forge, livetest.ProbePrelude(target, address), version, mode)); conclusion != "success" {
					t.Fatalf("publication job: %s", conclusion)
				}

				assertExportedPublications(t, target, repo, version, mode)
			})
		}
	}
}

func assertExportedPublications(t *testing.T, target livetest.Target, repo, version, mode string) {
	t.Helper()

	for _, kind := range []string{"jar", "aar"} {
		artifact := "fci-export-" + kind

		name := livetest.MavenPackageName(target, "com.example.livetest", artifact)
		if !slices.Contains(livetest.PublishedPackageVersions(t, target, repo, "maven", name), version) {
			t.Fatalf("%s publication missing from registry API", kind)
		}

		base := "com/example/livetest/" + artifact + "/" + version + "/"

		fileVersion := version
		if mode == "snapshot" {
			fileVersion = snapshotFileVersion(t, livetest.MavenRegistryFile(t, target, repo, base+"maven-metadata.xml"), kind)
		}

		body := livetest.MavenRegistryFile(t, target, repo, base+artifact+"-"+fileVersion+"."+kind)
		assertPublicationPayload(t, body, kind+":"+version)
	}
}

func snapshotFileVersion(t *testing.T, body []byte, kind string) string {
	t.Helper()

	var metadata struct {
		Build    int `xml:"versioning>snapshot>buildNumber"`
		Versions []struct {
			Extension  string `xml:"extension"`
			Classifier string `xml:"classifier"`
			Value      string `xml:"value"`
		} `xml:"versioning>snapshotVersions>snapshotVersion"`
	}
	if err := xml.Unmarshal(body, &metadata); err != nil {
		t.Fatal(err)
	}

	if metadata.Build != 2 {
		t.Fatalf("snapshot metadata build number = %d, want 2", metadata.Build)
	}

	for _, version := range metadata.Versions {
		if version.Extension == kind && version.Classifier == "" && strings.HasSuffix(version.Value, "-2") {
			return version.Value
		}
	}

	t.Fatalf("snapshot metadata does not resolve the second %s upload", kind)

	return ""
}

func assertPublicationPayload(t *testing.T, body []byte, expected string) {
	t.Helper()

	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}

	file, err := archive.Open("payload.txt")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != expected {
		t.Fatalf("registry served different publication bytes: %q", data)
	}
}
