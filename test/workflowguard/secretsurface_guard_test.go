// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"regexp"
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// declaredJobSecretReferences is, per job of the workflows that hold release
// signing material, the release token or a registry write credential (the
// container publisher, snapshot images and promotion), the exact set of
// `secrets.*`, `secrets['…']` and
// `github.token` references the job may make anywhere in its text: env, with,
// if, run, or a `secrets:` block of a nested call.
//
// job-privileges.json pins what the token may do; this pins which material a
// job can reach at all. The two signers are the point: the GPG signer must
// never gain a Cosign key, the Cosign signer must never gain the OpenPGP key,
// and the publisher must hold no signing secret. The orchestrator forwards
// larger sets, but only a reference in the leaf job makes a secret usable
// there, so the leaves are what is listed. A job absent from this table, or a
// reference absent from its row, fails the guard: widening is a diff here.
func declaredJobSecretReferences() map[string]map[string][]string {
	return map[string]map[string][]string{
		"release-create-github.yml": {
			"resolve-bootstrap":  nil,
			"generate-changelog": nil,
			"assemble-release":   {"github.token"},
			"sign-release-gpg": {
				"github.token",
				"secrets.RELEASE_GPG_PASSPHRASE",
				"secrets.RELEASE_GPG_PRIVATE_KEY",
				"secrets.RELEASE_GPG_PUBLIC_KEY",
			},
			// The public release key verifies the signed tag (late-authorization)
			// in every job that acts on it; it is not signing material.
			"sign-release": {
				"github.token",
				"secrets.RELEASE_COSIGN_PASSPHRASE",
				"secrets.RELEASE_COSIGN_PRIVATE_KEY",
				"secrets.RELEASE_GPG_PUBLIC_KEY",
			},
			"create-release": {"github.token", "secrets.RELEASE_GPG_PUBLIC_KEY", "secrets.RELEASE_TOKEN"},
		},
		"release-prepare-stage.yml": {
			"resolve-bootstrap":          nil,
			"generate-full-changelog":    nil,
			"generate-minimal-changelog": nil,
			"prepare-version-files":      {"github.token"},
			"version-bump": {
				"github.token",
				"secrets.RELEASE_GPG_PASSPHRASE",
				"secrets.RELEASE_GPG_PRIVATE_KEY",
				"secrets.RELEASE_SSH_SIGNING_KEY",
				"secrets.RELEASE_TOKEN",
			},
			"summarize-prepare-stage": nil,
		},
		"version-bump.yml": {
			"resolve-bootstrap":          nil,
			"generate-full-changelog":    nil,
			"generate-minimal-changelog": nil,
			"prepare-version-files":      {"github.token"},
			"bump-version": {
				"github.token",
				"secrets.RELEASE_GPG_PASSPHRASE",
				"secrets.RELEASE_GPG_PRIVATE_KEY",
				"secrets.RELEASE_SSH_SIGNING_KEY",
				"secrets.RELEASE_TOKEN",
			},
		},
		"generate-changelog.yml": {
			"resolve-bootstrap":  nil,
			"generate-changelog": {"github.token"},
		},
		"publish-container.yml": {
			"resolve-bootstrap": nil,
			"prep":              {"secrets.RELEASE_COSIGN_PASSPHRASE", "secrets.RELEASE_COSIGN_PRIVATE_KEY"},
			"build-arch":        {"github.token", "secrets.FORGE_CI_BUILD_SECRETS_JSON", "secrets.GITHUB_TOKEN", "secrets['registry-password']"},
			"scan-layouts":      {"github.token", "secrets.CODE_SCANNING_TOKEN"},
			"publish-images":    {"github.token", "secrets.GITHUB_TOKEN", "secrets['registry-password']"},
			"image-evidence":    {"secrets.GITHUB_TOKEN", "secrets.RELEASE_COSIGN_PASSPHRASE", "secrets.RELEASE_COSIGN_PRIVATE_KEY", "secrets['registry-password']"},
			"merge":             {"github.token", "secrets.GITHUB_TOKEN", "secrets.RELEASE_COSIGN_PASSPHRASE", "secrets.RELEASE_COSIGN_PRIVATE_KEY", "secrets['registry-password']"},
		},
		"snapshot-images.yml": {
			"resolve-bootstrap": nil,
			"plan":              nil,
			"build-artifacts":   nil,
			// Runs the consumer's build for a file output: no token, no secret.
			"build-files":     nil,
			"prepare-context": {"github.token"},
			"build":           nil,
			"publish":         {"github.token", "secrets.registry-password"},
			"collect":         nil,
		},
		"promote-stage.yml": {
			"resolve-bootstrap": nil,
			"promote":           {"github.token", "secrets.GITHUB_TOKEN", "secrets['registry-password']", "secrets['stage-registry-password']"},
		},
		"validate-release-prerequisites.yml": {
			"resolve-bootstrap": nil,
			"validate": {
				"github.token",
				"secrets.CODE_SCANNING_TOKEN",
				"secrets.MAVEN_CENTRAL_PASSWORD",
				"secrets.MAVEN_CENTRAL_USERNAME",
				"secrets.NPM_TOKEN",
				"secrets.RELEASE_GPG_PASSPHRASE",
				"secrets.RELEASE_GPG_PRIVATE_KEY",
				"secrets.RELEASE_GPG_PUBLIC_KEY",
				"secrets.RELEASE_TOKEN",
			},
		},
	}
}

var secretReferencePattern = regexp.MustCompile(`secrets\.[A-Za-z0-9_-]+|secrets\[['"][A-Za-z0-9_-]+['"]\]|github\.token`)

func TestJobSecretReferencesAreDeclared(t *testing.T) {
	t.Parallel()

	for file, declared := range declaredJobSecretReferences() {
		root, err := effectiveWorkflow(reporoot.ReadFile(t, ".github/workflows/"+file))
		require.NoError(t, err, file)

		jobs := mappingChild(root, "jobs")
		require.NotNil(t, jobs, file)

		var names []string

		for i := 0; i+1 < len(jobs.Content); i += 2 {
			name, job := jobs.Content[i].Value, jobs.Content[i+1]
			names = append(names, name)

			want, ok := declared[name]
			require.Truef(t, ok, "%s job %s is not in declaredJobSecretReferences; list the secrets it may reference", file, name)

			found := map[string]bool{}
			collectSecretReferences(job, found)

			if scalarChild(job, "secrets") == "inherit" {
				found["secrets: inherit"] = true
			}

			got := slices.Sorted(func(yield func(string) bool) {
				for reference := range found {
					if !yield(reference) {
						return
					}
				}
			})

			require.Equalf(t, want, nilIfEmpty(got), "%s job %s references other material than declared", file, name)
		}

		slices.Sort(names)

		declaredNames := slices.Sorted(func(yield func(string) bool) {
			for name := range declared {
				if !yield(name) {
					return
				}
			}
		})
		require.Equalf(t, declaredNames, names, "%s: the declared job set and the workflow's jobs differ", file)
	}
}

func collectSecretReferences(node *yaml.Node, found map[string]bool) {
	if node == nil {
		return
	}

	if node.Kind == yaml.ScalarNode {
		for _, match := range secretReferencePattern.FindAllString(node.Value, -1) {
			found[match] = true
		}

		return
	}

	for _, child := range node.Content {
		collectSecretReferences(child, found)
	}
}

func nilIfEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	return values
}
