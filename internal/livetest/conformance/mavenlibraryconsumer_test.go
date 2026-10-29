// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// TestForgejoMavenLibraryConsumer_Stable uses the existing consumer-trial
// authority and lifecycle, with a private-index CI adapter for an unmodified
// real library checkout. It intentionally judges only the signed file release.
func TestForgejoMavenLibraryConsumer_Stable(t *testing.T) {
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("FORGE_CI_LIVE_CONSUMER is not set")
	}
	if livetest.DemoName() != "" {
		t.Fatal("library acceptance requires disposable scratch mode")
	}
	var words []string
	for _, word := range strings.Split(os.Getenv(consumerSettingsEnv), "\n") {
		if word != "" {
			words = append(words, word)
		}
	}
	settings, err := livetest.ParseTrialSettings(livetest.TrialRelease, words)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Workflow != "release.yml" || settings.NextVersion != "" || settings.InterruptAfter != "" || len(settings.Images) != 0 || len(settings.TokenSecrets) != 0 {
		t.Fatal("this focused case requires one uninterrupted release.yml file release")
	}
	image, profile := settings.Variables["MAVEN_LIBRARY_IMAGE"], settings.Variables["MAVEN_LIBRARY_PROFILE"]
	delete(settings.Variables, "MAVEN_LIBRARY_IMAGE")
	delete(settings.Variables, "MAVEN_LIBRARY_PROFILE")
	if image == "" || profile == "" {
		t.Fatal("settings require MAVEN_LIBRARY_IMAGE=<digest-pinned Maven/JDK21 image> and MAVEN_LIBRARY_PROFILE=<project profile>")
	}
	recipe, err := filepath.Abs("../../../test/live/demos/hello-maven/consumer")
	if err != nil {
		t.Fatal(err)
	}
	target := livetest.Accept(t, provider.ForgeForgejo)
	if !livetest.Requires(t, provider.ForgeForgejo, livetest.NeedsInRunner) {
		t.Skip("Forgejo runner is unavailable")
	}
	settings.Variables, err = livetest.NativeReleasePlatforms(settings.Variables, livetest.RunnerArch(t, target))
	if err != nil {
		t.Fatal(err)
	}
	consumer := livetest.OpenConsumer(t, target, root)
	sourceCommit := consumer.Commit
	artifact := livetest.MavenLibraryArtifact(t, consumer)
	consumer = livetest.NewMavenLibraryConsumerFixture(t, consumer, livetest.MavenLibraryFixtureConfig{RecipeDirectory: recipe, JavaImage: image, Profile: profile})
	const name = "maven-library-consumer-stable"
	scenario := layerScenario{target: target, label: livetest.RunnerLabel(t, target)}
	scenario.layer, scenario.repo = newConsumerRepos(t, target, consumer, name, false)
	repo := scenario.repo
	facts := livetest.ConsumerFacts{}
	keys := trialReleaseFacts(t, target, repo, &facts)
	facts.Inputs["forge-ci-repository"] = scenario.layer.Repository
	requester := livetest.NewRequestSigner(t, t.TempDir())
	plan, err := scenario.layer.PlanConsumer(consumer.Workflows(t), facts)
	if err != nil {
		t.Fatal(err)
	}
	consumer.Push(t, repo, plan)
	main := livetest.CommitFiles(t, target, repo, livetest.FileCommit{Branch: layerMain, Message: "test: trust the disposable release requester", Files: map[string]string{trialRequestSigners: requester.AllowedSigners}})
	variables := map[string]string{layerRunnerVariable: runnerLabelsJSON(t, scenario.label), "FORGE_CI_REPOSITORY": scenario.layer.Repository}
	for _, more := range []map[string]string{plan.Variables, settings.ResolvedVariables(scenario.layer.Commit, main)} {
		for key, value := range more {
			if _, exists := variables[key]; exists {
				t.Fatalf("duplicate variable %s", key)
			}
			variables[key] = value
		}
	}
	livetest.AddDeployKey(t, target, repo, "livetest release bot", keys.sshPublic)
	livetest.EnableActions(t, target, repo)
	for key, value := range variables {
		livetest.SetRepoVariable(t, target, repo, key, value)
	}
	for key, value := range plan.Secrets {
		livetest.SetRepoSecret(t, target, repo, key, value)
	}
	requestTag, tag := "release-request/v"+settings.Version, "v"+settings.Version
	requester.PushTag(t, target, repo, requestTag, layerMain, "Request release "+tag)
	run := livetest.WaitForConsumerRun(t, target, repo, settings.Workflow, "push", "")
	scenario.logJobs(t, livetest.RunJobs(t, target, repo, run.ID), facts.Secrets, plan.Secrets)
	if run.Status != jobSuccess {
		t.Fatalf("library stable run %d ended %q", run.ID, run.Status)
	}
	requireReleaseGit(t, target, repo, keys, main, requestTag, tag)
	requireMavenLibraryFiles(t, target, repo, keys, tag, artifact)
	requireSignedRelease(t, target, repo, keys, tag)
	t.Logf("real library stable signed file release: source %s at %s, engine %s, run %d; package registry, fresh Maven resolution and dev are outside this verdict", root, sourceCommit, scenario.layer.Commit, run.ID)
}

func requireMavenLibraryFiles(t *testing.T, target livetest.Target, repo string, keys releaseKeys, tag, artifact string) {
	t.Helper()
	version := strings.TrimPrefix(tag, "v")
	stem := artifact + "-" + version
	var listed []livetest.ReleaseAsset
	for _, release := range livetest.Releases(t, target, repo) {
		if release.TagName == tag {
			listed = append(listed, release.Assets...)
		}
	}
	assets, err := livetest.MavenLibraryReleaseInventory(listed, artifact, version)
	if err != nil {
		t.Fatal(err)
	}
	payloads := map[string][]byte{}
	for _, suffix := range []string{".pom", ".jar", "-sources.jar", "-javadoc.jar"} {
		name := stem + suffix
		if assets[name] == "" {
			t.Fatalf("real library release missing %s", name)
		}
		payloads[name] = livetest.Download(t, target, assets[name], maxReleaseFile)
		if len(payloads[name]) == 0 {
			t.Fatalf("empty downloaded artifact %s", name)
		}
	}
	var publishedPOM struct {
		Artifact string `xml:"artifactId"`
		Version  string `xml:"version"`
	}
	if err := xml.Unmarshal(payloads[stem+".pom"], &publishedPOM); err != nil {
		t.Fatal(err)
	}
	if publishedPOM.Version != strings.TrimPrefix(tag, "v") || publishedPOM.Artifact+"-"+publishedPOM.Version != stem {
		t.Fatalf("published POM coordinates/version disagree with prepared release: %+v", publishedPOM)
	}
	// Verify a signed checksum document actually binds each of the four bytes,
	// rather than accepting an unrelated valid bundle as library evidence.
	covered := map[string]bool{}
	for name, address := range assets {
		plain, bundle := strings.CutSuffix(name, bundleSuffix)
		if !bundle || assets[plain] == "" {
			continue
		}
		body, direct := payloads[plain]
		if !direct {
			body = livetest.Download(t, target, assets[plain], maxReleaseFile)
		}
		if err := livetest.VerifyBlob(t, target, keys.cosignPublicPath, body, livetest.Download(t, target, address, maxReleaseFile)); err != nil {
			t.Fatal(err)
		}
		if direct {
			covered[plain] = true
		}
		for _, line := range strings.Split(string(body), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			file := strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "dist/")
			data, exists := payloads[file]
			if exists && fields[0] == fmt.Sprintf("%x", sha256.Sum256(data)) {
				covered[file] = true
			}
		}
	}
	for name := range payloads {
		if !covered[name] {
			t.Errorf("artifact %s is not bound by a verified signature/checksum", name)
		}
	}
	for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar"} {
		archive, err := zip.NewReader(bytes.NewReader(payloads[stem+suffix]), int64(len(payloads[stem+suffix])))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, file := range archive.File {
			if suffix == "-sources.jar" && strings.HasSuffix(file.Name, ".java") {
				found = true
			}
			if suffix == "-javadoc.jar" && file.Name == "index.html" {
				found = true
			}
			if suffix != ".jar" || !strings.HasSuffix(file.Name, ".class") || strings.HasPrefix(file.Name, "META-INF/") {
				continue
			}
			found = true
			stream, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			var header [8]byte
			_, err = io.ReadFull(stream, header[:])
			_ = stream.Close()
			if err != nil || binary.BigEndian.Uint32(header[:4]) != 0xcafebabe || binary.BigEndian.Uint16(header[6:]) != 65 {
				t.Errorf("%s is not Java21 bytecode", file.Name)
			}
		}
		if !found {
			t.Errorf("%s has no expected library content", stem+suffix)
		}
	}
}
