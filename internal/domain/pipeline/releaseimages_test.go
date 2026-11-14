// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

func plannedContainers(t *testing.T, containers string) []pipeline.PlannedContainer {
	t.Helper()

	cfg, err := config.Parse([]byte("artifacts:\n  - name: app\n    project-type: go\ncontainers:\n" + containers))
	if err != nil {
		t.Fatal(err)
	}

	if err = config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	if err = config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	return pipeline.NewConfigPlan(cfg).Containers.All
}

func releaseImagesInput() pipeline.ReleaseImagesInput {
	return pipeline.ReleaseImagesInput{ImageRepository: "ghcr.io/acme/app", Repository: "Acme/App", ReleaseTag: "v1.2.3"}
}

// Two containers share one repository as tag variants: each gets its own
// staging candidate, its final tag, and one build per selected architecture.
func TestPlanReleaseImages_PublishesTagVariantsOfOneRepository(t *testing.T) {
	t.Parallel()

	containers := plannedContainers(t, `  - name: app
    container-file: Containerfile.alpine
    platforms: linux/amd64,linux/arm64
    build-args: {B: two, A: one}
  - name: app-distroless
    tag-suffix: -distroless
    platforms: linux/amd64,linux/arm64
`)

	in := releaseImagesInput()
	in.ReleasePlatforms = "linux/arm64"

	plan, err := pipeline.PlanReleaseImages(containers, in)
	if err != nil {
		t.Fatal(err)
	}

	want := []pipeline.ReleaseImage{
		{Name: "app", ImageRepository: "ghcr.io/acme/app", CandidateTag: "staging-v1.2.3", FinalTagName: "v1.2.3", Archs: "arm64", Scan: true},
		{Name: "app-distroless", ImageRepository: "ghcr.io/acme/app", CandidateTag: "staging-v1.2.3-distroless", FinalTagName: "v1.2.3-distroless", Archs: "arm64", Scan: true},
	}
	if len(plan.Images) != len(want) || plan.Images[0] != want[0] || plan.Images[1] != want[1] {
		t.Fatalf("images = %+v, want %+v", plan.Images, want)
	}

	if len(plan.Builds) != 2 {
		t.Fatalf("builds = %+v, want one arm64 build per container", plan.Builds)
	}

	first := plan.Builds[0]
	if first.Arch != "arm64" || first.Platform != "linux/arm64" || first.ContainerFile != "Containerfile.alpine" || first.Context != "." || first.BuildArgs != "A=one\nB=two" || first.CandidateTag != "staging-v1.2.3" {
		t.Fatalf("first build = %+v", first)
	}
}

// A declared platforms is one list however it is separated, as on every
// other route that reads it.
func TestPlanReleaseImages_ReadsThePlatformsAsOneList(t *testing.T) {
	t.Parallel()

	for platforms, want := range map[string]string{
		"":                         "amd64",
		"linux/amd64,linux/arm64":  "amd64\narm64",
		"linux/amd64 linux/arm64":  "amd64\narm64",
		"linux/amd64, linux/arm64": "amd64\narm64",
	} {
		containers := "  - name: app\n"
		if platforms != "" {
			containers += "    platforms: " + platforms + "\n"
		}

		plan, err := pipeline.PlanReleaseImages(plannedContainers(t, containers), releaseImagesInput())
		if err != nil || plan.Images[0].Archs != want {
			t.Errorf("platforms %q: plan = %+v, err = %v, want archs %q", platforms, plan.Images, err, want)
		}
	}
}

// A release publishes to one full repository, written out: a registry or
// owner is never inferred.
func TestPlanReleaseImages_PublishesToTheOneFullRepository(t *testing.T) {
	t.Parallel()

	containers := plannedContainers(t, "  - name: app\n")

	for repository, want := range map[string]string{
		"registry.example:5000/Team/App": "registry.example:5000/team/app",
		"localhost/app":                  "localhost/app",
		"acme/app":                       "",
		"app":                            "",
	} {
		in := releaseImagesInput()
		in.ImageRepository = repository

		plan, err := pipeline.PlanReleaseImages(containers, in)
		if want == "" {
			if !errors.Is(err, errs.ErrUsage) {
				t.Errorf("%s: err = %v, want a refusal of a repository without its registry", repository, err)
			}

			continue
		}

		if err != nil || plan.Images[0].ImageRepository != want {
			t.Errorf("%s: plan = %+v, err = %v, want %s", repository, plan.Images, err, want)
		}
	}
}

// Release images gate at the default threshold, CRITICAL, however an adopter
// spells it; a stricter or looser threshold is refused above.
func TestPlanReleaseImages_AcceptsTheDefaultScanThreshold(t *testing.T) {
	t.Parallel()

	for _, severity := range []string{"CRITICAL", "critical", " CRITICAL "} {
		containers := plannedContainers(t, "  - name: app\n    scan-severity: \""+severity+"\"\n")
		if _, err := pipeline.PlanReleaseImages(containers, releaseImagesInput()); err != nil {
			t.Errorf("scan-severity %q: err = %v, want the default threshold accepted", severity, err)
		}
	}
}

func TestPlanReleaseImages_RefusesWhatItDoesNotCarryOut(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		containers string
		tag        string
		platforms  string
		want       string
	}{
		"same final tag":   {containers: "  - name: app\n  - name: other\n", want: "would both publish ghcr.io/acme/app:v1.2.3"},
		"arch tag":         {containers: "  - name: app\n  - name: other\n    tag-suffix: -amd64\n", want: "would both publish ghcr.io/acme/app:staging-v1.2.3-amd64"},
		"image-name":       {containers: "  - name: app\n    image-name: acme/other\n", want: "tag-suffix"},
		"build secrets":    {containers: "  - name: app\n    build-secrets: [PRIVATE_REGISTRY_CREDENTIAL]\n", want: "build-secrets"},
		"extraction":       {containers: "  - name: app\n    extract: {binary: {paths: [/app]}}\n", want: "extract"},
		"scan threshold":   {containers: "  - name: app\n    scan-severity: CRITICAL,HIGH\n", want: "gate at CRITICAL;"},
		"foreign platform": {containers: "  - name: app\n    platforms: linux/s390x\n", want: "linux/amd64 and linux/arm64"},
		"undeclared":       {containers: "  - name: app\n", platforms: "linux/arm64", want: "not declared"},
		"build metadata":   {containers: "  - name: app\n", tag: "v1.2.3+build", want: "release tag"},
		"not a version":    {containers: "  - name: app\n", tag: "latest", want: "release tag"},
		"outside file":     {containers: "  - name: app\n    container-file: ../layer/Containerfile\n", want: "inside the repository"},
		"absolute context": {containers: "  - name: app\n    context: /etc\n", want: "inside the repository"},
	} {
		in := releaseImagesInput()
		in.ReleasePlatforms = tc.platforms

		if tc.tag != "" {
			in.ReleaseTag = tc.tag
		}

		_, err := pipeline.PlanReleaseImages(plannedContainers(t, tc.containers), in)
		refused := errors.Is(err, errs.ErrInvalidConfig) || errors.Is(err, errs.ErrUsage)

		if !refused || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want a refusal naming %q", name, err, tc.want)
		}
	}

	if _, err := pipeline.PlanReleaseImages(nil, releaseImagesInput()); !errors.Is(err, errs.ErrInvalidConfig) {
		t.Errorf("no containers: err = %v, want ErrInvalidConfig", err)
	}
}

// The release's labels go on every build after the standard ones, as given;
// one that would replace a standard provenance label is refused, as is a
// key, value or repetition no image could carry.
func TestPlanReleaseImages_PlansTheReleaseLabels(t *testing.T) {
	t.Parallel()

	containers := plannedContainers(t, `  - name: app
    platforms: linux/amd64,linux/arm64
`)

	in := releaseImagesInput()
	in.Labels = []string{"org.opencontainers.image.licenses=EUPL-1.2 OR MIT", "org.opencontainers.image.vendor=Acme, Inc."}

	plan, err := pipeline.PlanReleaseImages(containers, in)
	if err != nil {
		t.Fatal(err)
	}

	want := "org.opencontainers.image.licenses=EUPL-1.2 OR MIT\norg.opencontainers.image.vendor=Acme, Inc."
	for _, build := range plan.Builds {
		if build.Labels != want {
			t.Fatalf("%s labels = %q, want %q", build.Arch, build.Labels, want)
		}
	}

	refused := map[string][]string{
		"standard":  {"org.opencontainers.image.source=https://elsewhere.example/repo"},
		"created":   {"org.opencontainers.image.created=2020-01-01T00:00:00Z"},
		"no value":  {"org.opencontainers.image.licenses="},
		"no equals": {"org.opencontainers.image.licenses"},
		"key":       {"Org.Example=1"},
		"newline":   {"org.example.a=1\norg.opencontainers.image.revision=0"},
		"twice":     {"org.example.a=1", "org.example.a=2"},
	}
	for name, labels := range refused {
		in.Labels = labels
		if _, err := pipeline.PlanReleaseImages(containers, in); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("%s: %q planned, err = %v; want a usage error", name, labels, err)
		}
	}
}
