// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// TestForgejoLayer_ResolvedImagePlan uses two tiny bases and a final-release
// companion across three sequential runs in one public SHA-256 consumer.
// The publisher alone populates cache slots; a content change invalidates one
// slot before the warm repeat.
func TestForgejoLayer_ResolvedImagePlan(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	jobImage := os.Getenv("FORGE_CI_LIVE_IMAGE_PLAN_JOB_IMAGE")
	if jobImage == "" {
		t.Skip("set FORGE_CI_LIVE_IMAGE_PLAN_JOB_IMAGE to the lab kaniko job image for this focused scenario")
	}

	target := livetest.Accept(t, provider.ForgeForgejo)
	if livetest.RunnerArch(t, target) != "amd64" {
		t.Skip("this focused image-filesystem canary currently verifies linux/amd64")
	}

	registry, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(jobImage, registry+"/") || !strings.Contains(jobImage, "@sha256:") {
		t.Fatal("the focused scenario requires a digest-pinned job image on the admitted lab registry")
	}

	// The admitted job image names this exact scratch layer coordinate.
	scenario := newLayerScenario(t, livetest.ConsumerTrial, false, "sha256")
	repo := scenario.repo
	livetest.SweepPackagesAfter(t, target, repo)
	livetest.SetRepoVariable(t, target, repo, "CI_IMAGE_BUILDER", "kaniko")
	livetest.SetRepoVariable(t, target, repo, "CI_IMAGE_BUILDER_JOB_IMAGE", jobImage)
	livetest.SetRepoVariable(t, target, repo, "RELEASE_REGISTRY_USER", "forgeci")
	livetest.SetRepoSecret(t, target, repo, "RELEASE_REGISTRY_TOKEN", target.Token)
	baseRepository := registry + "/" + strings.ToLower(livetest.RepoSlug(target, repo)) + "-base"

	slots := make(map[string]string, 2)

	for _, name := range []string{"steady", "changing"} {
		if _, found := livetest.ImageDigest(t, target, repo+"-base", "cache-arch-"+name+"-amd64"); found {
			t.Fatal("cold phase requires absent scratch cache slots")
		}
	}

	var (
		previous map[string]imageledger.Entry
		afterID  int64
	)

	for _, phase := range []string{"cold", "partial", "warm"} {
		files := map[string]string{"canary-phase": phase + "\n"}
		if phase == "cold" {
			files[".forgejo/workflows/resolved-image-plan.yml"] = scenario.render(t, resolvedImagePlanFixture,
				"RENDER_BASE_REPOSITORY", baseRepository)
			for _, name := range []string{"steady", "changing"} {
				files[name+"/Containerfile"] = resolvedImagePlanContainerfile
				files[name+"/marker.sh"] = imagePlanMarker(name, "v1")
			}
		}

		if phase == "partial" {
			files["changing/marker.sh"] = imagePlanMarker("changing", "v2")
		}

		livetest.CommitFiles(t, target, repo, livetest.FileCommit{
			Branch: layerMain, Message: "test: resolved image cache " + phase, Files: files,
		})
		run := livetest.WaitForConsumerRunAfter(t, target, repo, "resolved-image-plan.yml", "push", afterID)
		jobs := scenario.requireStatus(t, run, jobSuccess)
		afterID = run.ID
		artifactName := "resolved-base-review-" + phase

		tree, err := imagePlanArtifactTree(livetest.RunArtifacts(t, target, repo, run.ID), artifactName, maxReleaseFile,
			func(id int64) []byte { return livetest.ArtifactZIP(t, target, repo, id) })
		if err != nil {
			t.Fatalf("run %d review artifacts: %v", run.ID, err)
		}

		entries, err := imagePlanReview(tree, baseRepository)
		if err != nil {
			t.Fatalf("%s run %d artifact %s: %v", phase, run.ID, artifactName, err)
		}

		for _, name := range []string{"steady", "changing"} {
			entry := entries[name]
			hit := phase == "warm" || (phase == "partial" && name == "steady")

			pair, err := imagePlanJobs(jobs, name)
			if err != nil {
				t.Fatalf("%s run %d: %v: %+v", phase, run.ID, err, jobs)
			}

			evidence, err := imagePlanCacheEvidence(livetest.JobLog(t, target, repo, pair.fetch), livetest.JobLog(t, target, repo, pair.build), baseRepository+"@"+slots[name], hit)
			if err != nil {
				t.Fatalf("%s run %d fetch job %d build job %d: %v", phase, run.ID, pair.fetch.ID, pair.build.ID, err)
			}

			t.Logf("cache execution verified: phase=%s run=%d fetch=%d build=%d image=%s hit=%t evidence=%s", phase, run.ID, pair.fetch.ID, pair.build.ID, name, hit, evidence)

			candidate := "staging-" + entry.BaseInputID + "-" + name
			served, found := livetest.ImageDigest(t, target, repo+"-base", candidate)

			if !found || served != entry.Digest {
				t.Fatal("the namespaced ledger does not identify the registry-served candidate")
			}

			version := "v1"
			if phase != "cold" && name == "changing" {
				version = "v2"
			}

			hashes := livetest.ImageFileSHA256(t, target, repo+"-base", entry.Digest, []string{"/marker.sh"})
			wantMarker := imagePlanSHA256([]byte(imagePlanMarker(name, version)))

			if hashes["marker.sh"] != wantMarker {
				t.Fatal("the actual registry image does not contain the selected target's canary bytes")
			}

			slot, found := livetest.ImageDigest(t, target, repo+"-base", "cache-arch-"+name+"-amd64")
			if !found {
				t.Fatal("the gated publisher did not populate its architecture cache slot")
			}

			if phase != "cold" {
				unchanged := entry.BaseInputID == previous[name].BaseInputID && entry.Digest == previous[name].Digest && slot == slots[name]
				if hit != unchanged || (!hit && (entry.BaseInputID == previous[name].BaseInputID || slot == slots[name])) {
					t.Fatalf("%s %s: content/slot transition disagrees with hit=%t", phase, name, hit)
				}
			}

			slots[name] = slot
			t.Logf("resolved image plan verified: phase=%s run=%d image=%s cache-child=%s@%s marker-sha256=%s sbom-sha256=%s artifact=%s", phase, run.ID, entry.Ref, baseRepository, slot, wantMarker, entry.SBOMSHA256, artifactName)
		}

		previous = entries

		finalArtifact := "resolved-final-review-" + artifactName

		finalTree, err := imagePlanArtifactTree(livetest.RunArtifacts(t, target, repo, run.ID), finalArtifact, maxReleaseFile,
			func(id int64) []byte { return livetest.ArtifactZIP(t, target, repo, id) })
		if err != nil {
			t.Fatalf("run %d final review artifacts: %v", run.ID, err)
		}

		urlPrefix := strings.TrimRight(target.BaseURL(), "/") + "/" + livetest.RepoSlug(target, repo) + "/releases/download/v0.0.1"

		final, child, err := imagePlanFinalReview(finalTree, baseRepository, urlPrefix)
		if err != nil {
			t.Fatalf("%s run %d artifact %s: %v", phase, run.ID, finalArtifact, err)
		}

		// The final companion never has a cache slot: its fetch job reports
		// no reuse and its build job executes once.
		pair, err := imagePlanJobs(jobs, "final")
		if err != nil {
			t.Fatalf("run %d: %v: %+v", run.ID, err, jobs)
		}

		if _, err := imagePlanCacheEvidence(livetest.JobLog(t, target, repo, pair.fetch), livetest.JobLog(t, target, repo, pair.build), "", false); err != nil {
			t.Fatalf("final run %d fetch job %d build job %d: %v", run.ID, pair.fetch.ID, pair.build.ID, err)
		}

		served, found := livetest.ImageDigest(t, target, repo+"-base", "staging-v0.0.1")
		if !found || served != final.Digest {
			t.Fatal("delivered final index bytes do not identify the registry-served candidate")
		}
		// Read the exact child bound by both retained index and BOM, not a
		// mutable tag. No container or product binary is executed.
		hashes := livetest.ImageFileSHA256(t, target, repo+"-base", child, []string{"/marker.sh"})
		if hashes["marker.sh"] != imagePlanSHA256([]byte(imagePlanMarker("steady", "v1"))) {
			t.Fatal("final index child does not contain the selected target's marker")
		}

		t.Logf("final release evidence verified: phase=%s run=%d index=%s child=%s inventory-sha256=%s index-bom-sha256=%s url-prefix=%s artifact=%s", phase, run.ID, final.Ref, child, imagePlanSHA256(finalTree["sbom-distroless-amd64.json"]), final.SBOMSHA256, urlPrefix, finalArtifact)
	}
}

// imagePlanArtifactTree shares only byte acquisition mechanics. A missing tree
// remains nil for the separate base/final review validators to reject. Listing
// and bounded download stay with the caller's exact target, repository and run.
func imagePlanArtifactTree(artifacts []livetest.RunArtifact, name string, limit int64, download func(int64) []byte) (map[string][]byte, error) {
	var tree map[string][]byte

	for _, artifact := range artifacts {
		if artifact.Name != name {
			continue
		}

		if tree != nil {
			return nil, fmt.Errorf("duplicate review artifact %q", name)
		}

		var err error

		tree, err = livetest.ZIPTree(download(artifact.ID), limit)
		if err != nil {
			return nil, err
		}
	}

	return tree, nil
}
