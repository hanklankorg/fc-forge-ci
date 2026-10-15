// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"slices"
	"strings"
	"testing"
)

const (
	buildReleaseImageBuild  = "Build, scan and publish image"
	buildReleaseImageRecord = "Record release image in ledger"
)

// The first engine call owns build/scan/publication ordering. The ledger records
// only its successful outputs; the caller supplies the required scan template.
func TestBuildReleaseImageHandsTheBuildToTheScanAndTheLedger(t *testing.T) {
	t.Parallel()
	action := loadAction(t, "build-release-image")
	action.requireSteps(t, 2)

	if id := action.Runs.Steps[0].ID; id != "build" {
		t.Fatalf("the build step has id %q, which the later steps read as steps.build", id)
	}

	action.requireStepEnv(t, 0, map[string]string{
		"IMAGE_TAG": "${{ inputs.tag }}", "CONTAINER_FILE": "${{ inputs.containerfile }}", "BUILDS_JSON": "${{ inputs.builds }}",
		"IMAGE_NAME": "${{ inputs.image }}", "BUILD_CONTEXT": "${{ inputs.context }}", "OCI_LABEL_TITLE": "${{ inputs.title }}",
		"OCI_LABEL_DESCRIPTION": "${{ inputs.description }}", "OCI_LABEL_LICENSES": "${{ inputs.licenses }}",
		"OCI_LABEL_VENDOR": "${{ inputs.vendor }}", "OCI_LABEL_AUTHORS": "${{ inputs.authors }}",
		"OCI_LABEL_DOCUMENTATION": "${{ inputs.documentation }}", "OCI_LABEL_REF_NAME": "${{ inputs.ref-name }}",
		"OCI_LABEL_VERSION": "${{ inputs.version }}", "OCI_LABEL_REVISION": "${{ inputs.revision }}",
		"FORGE_CI_REGISTRY_AUTH_FILE": "${{ inputs.auth-file }}",
		"SERVER_URL":                  "${{ github.server_url }}", "REPOSITORY": "${{ github.repository }}",
		"SCAN_OUTPUT": "${{ inputs.scan-output }}",
	})
	action.requireStepEnv(t, 1, map[string]string{
		"RECORD_ROLE": "${{ inputs.role }}", "RECORD_IMAGE": "${{ steps.build.outputs.image }}",
		"RECORD_DIGEST": "${{ steps.build.outputs.digest }}", "RECORD_TAG": "${{ inputs.tag }}",
		"RECORD_RELEASE_TAG": "${{ inputs.release-tag }}", "RECORD_SBOM": "${{ inputs.sbom }}",
	})

	for _, required := range []string{"tag", "containerfile", "builds", "role", "scan-output", "title"} {
		if !action.Inputs[required].Required || action.Inputs[required].Default != nil {
			t.Errorf("input %s must be required and without a default", required)
		}
	}
}

// Each step forwards its values to the flags of one verb, and the scan and
// the ledger entry refuse a build step that emitted nothing before the
// engine starts.
func TestBuildReleaseImageEngineCalls(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		step     string
		env      []string
		argv     []string
		required []string
	}{
		{
			buildReleaseImageBuild,
			[]string{
				"IMAGE_TAG=tag-v", "CONTAINER_FILE=containerfile-v", "BUILDS_JSON=builds-v", "IMAGE_NAME=image-v",
				"BUILD_CONTEXT=context-v", "OCI_LABEL_TITLE=title-v", "OCI_LABEL_DESCRIPTION=description-v",
				"OCI_LABEL_LICENSES=licenses-v", "OCI_LABEL_VENDOR=vendor-v", "OCI_LABEL_AUTHORS=authors-v",
				"OCI_LABEL_DOCUMENTATION=documentation-v", "OCI_LABEL_REF_NAME=ref-name-v", "OCI_LABEL_VERSION=version-v",
				"OCI_LABEL_REVISION=revision-v", "FORGE_CI_REGISTRY_AUTH_FILE=auth-file-v",
				"SERVER_URL=server-url-v", "REPOSITORY=repository-v", "SCAN_OUTPUT=scan-output-v",
			},
			[]string{
				"container", "build-push-oci-image", "--tag", "tag-v", "--containerfile", "containerfile-v",
				"--builds-json", "builds-v", "--image", "image-v", "--context", "context-v", "--title", "title-v",
				"--description", "description-v", "--licenses", "licenses-v", "--vendor", "vendor-v",
				"--authors", "authors-v", "--documentation", "documentation-v", "--ref-name", "ref-name-v",
				"--version", "version-v", "--revision", "revision-v", "--auth-file", "auth-file-v",
				"--server-url", "server-url-v", "--repository", "repository-v", "--scan-output-template", "scan-output-v",
			},
			[]string{"SCAN_OUTPUT"},
		},
		{
			buildReleaseImageRecord,
			[]string{
				"RECORD_ROLE=role-v", "RECORD_IMAGE=image-v", "RECORD_DIGEST=digest-v", "RECORD_TAG=tag-v",
				"RECORD_RELEASE_TAG=release-tag-v", "RECORD_SBOM=sbom-v",
			},
			[]string{
				"container", "ledger", "add", "--role", "role-v", "--image-name", "image-v", "--digest", "digest-v",
				"--final-tag-name", "tag-v", "--sbom", "sbom-v", "--default-sbom", "--tag", "release-tag-v",
			},
			[]string{"RECORD_ROLE", "RECORD_IMAGE", "RECORD_DIGEST", "RECORD_TAG"},
		},
	} {
		t.Run(test.step, func(t *testing.T) {
			t.Parallel()
			script := actionStep(t, "build-release-image", test.step)

			box := newSandbox(t, "build-release-image")
			if got := box.run(script, test.env...); got.status != 0 || !slices.Equal(box.argv(), test.argv) {
				t.Fatalf("status %d argv %q, want %q; stderr %s", got.status, box.argv(), test.argv, got.stderr)
			}

			for _, name := range test.required {
				refused := newSandbox(t, "build-release-image")
				cleared := slices.Clone(test.env)

				for i, entry := range cleared {
					if strings.HasPrefix(entry, name+"=") {
						cleared[i] = name + "="
					}
				}

				if got := refused.run(script, cleared...); got.status == 0 || refused.argv() != nil || !strings.Contains(got.stderr, name) {
					t.Fatalf("an empty %s: status %d argv %q stderr %q", name, got.status, refused.argv(), got.stderr)
				}
			}
		})
	}
}

// The ledger step consumes the successfully published digest through the real CLI.
func TestBuildReleaseImageThroughTheEngine(t *testing.T) {
	t.Parallel()
	box, env := engineSandbox(t, "build-release-image", nil)

	const (
		image  = "forge.example/owner/app"
		digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)

	record := box.run(actionStep(t, "build-release-image", buildReleaseImageRecord), append(env,
		"RECORD_ROLE=distroless", "RECORD_IMAGE="+image, "RECORD_DIGEST="+digest, "RECORD_TAG=v1.2.3-distroless",
		"RECORD_RELEASE_TAG=v1.2.3", "RECORD_SBOM=")...)
	if record.status != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", record.status, record.stdout, record.stderr)
	}

	ledger := readSandboxFile(t, box, "dist/release-images.json")
	for _, want := range []string{`"distroless"`, image + "@" + digest, image + ":v1.2.3-distroless", "dist/image-sbom-distroless.cyclonedx.json"} {
		if !strings.Contains(ledger, want) {
			t.Errorf("the ledger entry lacks %s:\n%s", want, ledger)
		}
	}
}
