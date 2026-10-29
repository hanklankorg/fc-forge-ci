// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"gopkg.in/yaml.v3"
)

// Complete Nano signer call shape: destination, request authorization, trust
// root and the seven secret forwards must survive the scratch migration.
const publicationCaller = `jobs:
  sign-and-publish:
    uses: itiquette/forge-ci/.forgejo/workflows/sign-and-publish-release.yml@` + consumerPinned + `
    with:
      runner-label: ${{ vars.CI_RUNNER_LABEL }}
      release-tag: ${{ needs.prepare.outputs.release-tag }}
      release-sha: ${{ needs.prepare.outputs.release-sha }}
      release-request-sha: ${{ forgejo.sha }}
      dist-digest: ${{ needs.dist.outputs['dist_digest'] }}
      artifact-name: build-dist
      artifact-path: dist/
      release-images-path: dist/release-images.json
      base-repository: ${{ vars.BASE_IMAGE_REPOSITORY || 'ghcr.io/itiquette/nanolinter-base' }}
      publication-image-repository: ${{ vars.IMAGE_REPOSITORY || 'ghcr.io/itiquette/nanolinter' }}
      registry-user: ${{ vars.IMAGE_REGISTRY_USER }}
      caller-workflow: release.yml
      release-asset-retention: "6"
      forge-ci-repository: itiquette/forge-ci
      forge-ci-sha: ` + consumerPinned + `
      signer-image: ghcr.io/itiquette/forge-ci-signer@sha256:450d938265a1d322eb2d92def121875df607dfafc31b71241a712ce2e2c8d1b2
      cosign-public-key: ${{ vars.COSIGN_PUBLIC_KEY }}
      cosign-public-key-sha256: db37ab94d631f06c5c4a65d63d173b2e16f33a01797234bf301f0def7b09b0f9
    secrets:
      RELEASE_COSIGN_PRIVATE_KEY: ${{ secrets.RELEASE_COSIGN_PRIVATE_KEY }}
      RELEASE_COSIGN_PASSPHRASE: ${{ secrets.RELEASE_COSIGN_PASSPHRASE }}
      RELEASE_GPG_PRIVATE_KEY: ${{ secrets.RELEASE_GPG_PRIVATE_KEY }}
      RELEASE_GPG_FINGERPRINT: ${{ secrets.RELEASE_GPG_FINGERPRINT }}
      RELEASE_GPG_PASSPHRASE: ${{ secrets.RELEASE_GPG_PASSPHRASE }}
      RELEASE_TOKEN: ${{ secrets.RELEASE_API_TOKEN }}
      RELEASE_REGISTRY_TOKEN: ${{ secrets.PACKAGE_PUBLISH_TOKEN }}
`

func TestPublicationCallerContract(t *testing.T) {
	t.Parallel()
	migrated, err := migratePublicationRepository(publicationCaller)
	if err != nil {
		t.Fatal(err)
	}
	if migrated != strings.Replace(publicationCaller, "publication-image-repository:", "image-repository:", 1) {
		t.Fatal("migration changed destination value, authorization, trust or secret forwarding")
	}
	var caller struct {
		Jobs map[string]struct {
			With    map[string]yaml.Node
			Secrets map[string]string
		}
	}
	if err := yaml.Unmarshal([]byte(migrated), &caller); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../.forgejo/workflows/sign-and-publish-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var callee struct {
		On struct {
			Call struct {
				Inputs map[string]struct {
					Type     string
					Required bool
				}
			} `yaml:"workflow_call"`
		}
	}
	if err := yaml.Unmarshal(body, &callee); err != nil {
		t.Fatal(err)
	}
	job := caller.Jobs["sign-and-publish"]
	for name, value := range job.With {
		input, found := callee.On.Call.Inputs[name]
		if !found || input.Type != "string" || value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			t.Errorf("unsupported input %s", name)
		}
	}
	for name, input := range callee.On.Call.Inputs {
		if _, found := job.With[name]; input.Required && !found {
			t.Errorf("missing required input %s", name)
		}
	}
	var secrets []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "# workflow-call-secrets-contract:") {
			secrets = strings.Fields(strings.TrimPrefix(line, "# workflow-call-secrets-contract:"))
		}
	}
	if len(secrets) != len(job.Secrets) || len(secrets) == 0 {
		t.Fatal("secret contract differs")
	}
	for name := range job.Secrets {
		if !strings.Contains(" "+strings.Join(secrets, " ")+" ", " "+name+" ") {
			t.Errorf("unsupported secret %s", name)
		}
	}
	const destination = "registry.lab.test/owner/app"
	plan, err := sampleLayer().PlanConsumer(map[string]string{"release.yml": publicationCaller}, ConsumerFacts{Inputs: map[string]string{"image-repository": destination, "base-repository": destination + "-base"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Variables["IMAGE_REPOSITORY"] != destination || plan.Variables["BASE_IMAGE_REPOSITORY"] != destination+"-base" || strings.Contains(plan.Render(publicationCaller), "publication-image-repository:") {
		t.Fatal("scratch destination binding lost")
	}
}

func TestPublicationMigrationRejectsSplitAuthority(t *testing.T) {
	t.Parallel()
	if _, err := migratePublicationRepository(strings.Replace(publicationCaller, "      registry-user:", "      image-repository: other/authority\n      registry-user:", 1)); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("split authority admitted: %v", err)
	}
	other := strings.Replace(publicationCaller, "sign-and-publish-release.yml@", "release-images.yml@", 1)
	if got, err := migratePublicationRepository(other); err != nil || got != other {
		t.Fatal("unrelated workflow changed")
	}
}
