// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// A Gradle JVM library reaches a fresh Gradle consumer through the forge's own
// Maven registry.
//
// TestInRunner_ExportedMavenPublications uploads archives the job wrote by
// hand, so it shows the registry transport and no producer. Here Gradle is the
// producer: the fixture applies java-library and maven-publish and declares
// one publication, `build gradle export-publications` exports it through the
// project's own wrapper, and `publish forge-packages deploy --project-type
// gradle` uploads what was exported, checksum sidecars and Gradle module
// metadata included. Those are the phases docs/publishing.md (Gradle JVM and
// Android libraries) gives a Forgejo consumer to compose in its own job;
// there is no Forgejo Gradle
// workflow to call instead.
//
// The same job then runs a second Gradle build that names the library as a
// coordinate and the registry as its only repository, with no token and a
// Gradle home holding the distribution alone. It must print the greeting only
// the library's code returns, and its dependency cache must hold the
// producer's JAR and module metadata byte for byte.
//
// The host reads the registry without Gradle: the POM, the module metadata,
// the JAR and the sources JAR are the bytes the export produced, by the
// receipt frozen before the upload, and the module metadata offers a consumer
// that JAR by its size and digest.
//
// The wrapper downloads its pinned Gradle distribution from services.gradle.org
// inside the job, as it does in every Gradle demo: toolchain provisioning, like
// the job image. Neither build declares a dependency or a plugin outside
// Gradle itself, so every other request is to the lab.
func TestInRunner_ForgePackages_GradleLibraryReachesAFreshConsumer(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	const tag = "v0.0.11-pkggradle"

	target := livetest.Accept(t, provider.ForgeForgejo)
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner, livetest.NeedsPackages) {
		t.Skip("requires a Forgejo runner and package registry")
	}

	// Unique for the reason PAR-PKG-1 documents: the registry is the owner's.
	version := forgePackageVersion(time.Now())

	files, err := gradleLibraryFiles(version)
	if err != nil {
		t.Fatal(err)
	}

	repo := livetest.NewPublicScratchRepo(t, target, "pkggradle", livetest.ReadProbeAssetAnonymously)
	name := livetest.MavenPackageName(target, gradleLibraryGroup, gradleLibraryArtifact)

	if slices.Contains(livetest.PublishedPackageVersions(t, target, repo, "maven", name), version) {
		t.Fatalf("%s:%s exists before publishing, so finding it afterwards would prove nothing", name, version)
	}

	t.Cleanup(func() { livetest.DeletePublishedPackage(t, target, repo, "maven", name, version) })

	livetest.PrepareTag(t, target, repo, tag)
	livetest.PublishBinaryAsset(t, target, repo, tag, t.TempDir())
	livetest.SetRepoSecret(t, target, repo, packageTokenSecret, target.Token)

	// No workflow is committed yet, so this push starts no run.
	fixture := livetest.CommitFiles(t, target, repo, livetest.FileCommit{
		Branch: layerMain, Message: "test: add the Gradle library and its consumer", Files: files,
	})

	prelude := livetest.ProbePrelude(target, livetest.ReleaseAssetURL(t, target, repo, tag, "forge-ci"))
	if conclusion := livetest.RunWorkflow(t, target, repo, "forge-packages-gradle",
		gradleLibraryProbe(target, prelude, repo, fixture, version, tag)); conclusion != "success" {
		t.Fatalf("the publish/consume job concluded %q: the Gradle export, the upload, or the fresh consumer's resolution, output or resolved bytes failed", conclusion)
	}

	if after := livetest.PublishedPackageVersions(t, target, repo, "maven", name); !slices.Contains(after, version) {
		t.Errorf("the job succeeded but %s:%s is not in the forge's package registry (found %v)", name, version, after)
	}

	assertGradleLibraryPublication(t, target, repo, version, tag)
}

// assertGradleLibraryPublication reads the receipt the job froze before the
// upload and the registry's files, independently of Gradle and of the
// product's upload result.
func assertGradleLibraryPublication(t *testing.T, target livetest.Target, repo, version, tag string) {
	t.Helper()

	stem := gradleLibraryArtifact + "-" + version
	names := []string{stem + ".pom", stem + ".module", stem + ".jar", stem + "-sources.jar"}

	receipt, err := checksumReceipt(livetest.Download(t, target, livetest.ReleaseAssetURL(t, target, repo, tag, gradleLibraryReceipt), 4096), names...)
	if err != nil {
		t.Fatal(err)
	}

	base := strings.ReplaceAll(gradleLibraryGroup, ".", "/") + "/" + gradleLibraryArtifact + "/" + version + "/"
	registry := make(map[string][]byte, len(names))

	for _, name := range names {
		registry[name] = livetest.MavenRegistryFile(t, target, repo, base+name)
		if sha256Hex(registry[name]) != receipt[name] {
			t.Errorf("registry %s differs from the file Gradle exported", name)
		}
	}

	for _, finding := range gradlePublicationFindings(registry[stem+".module"], registry[stem+".pom"], registry[stem+".jar"], version) {
		t.Error(finding)
	}

	for name, entry := range map[string]string{
		stem + ".jar":         "org/example/fixture/gradlelibrary/Greeting.class",
		stem + "-sources.jar": "org/example/fixture/gradlelibrary/Greeting.java",
	} {
		archive, err := zip.NewReader(bytes.NewReader(registry[name]), int64(len(registry[name])))
		if err != nil {
			t.Fatalf("registry %s: %v", name, err)
		}

		if _, err := fs.Stat(archive, entry); err != nil {
			t.Errorf("registry %s lacks %s: %v", name, entry, err)
		}
	}
}
