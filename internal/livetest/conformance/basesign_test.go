// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

// PAR-SIGN-3: what `container base-images sign` writes, `container base-images
// promote` accepts, against a real registry.
//
// The signer exists for one reason: the base-image verifiers match a signature,
// a CycloneDX attestation and four lineage fields (source, workflow, flavor,
// base input ID) that somebody has to have attested. Its unit tests compare the
// cosign argv with what `container ledger sign` would have sent; only a registry
// can show that the engine's own verifier then finds all of it and agrees.
//
// `promote` is the verifier used, not `validate`. Both run the same evidence
// check, but `validate` reads the registry anonymously and exits 0 for a tag it
// cannot find, reporting a cache miss; `promote` logs in and fails closed.
// Given an image whose final tag already serves the signed digest and no
// candidate, it verifies and copies nothing.
//
// Containment is the suite's, as in PAR-SIGN-1: the transparency log is off for
// every invocation (see livetest/cli.go), and this scenario asserts it held.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

func TestSign_BaseImages_AreAcceptedByTheBaseImageVerifier(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	const (
		flavor   = "livetest"
		workflow = "build-base-images.yml"

		// An opaque pin to the engine, which checks only its shape: the
		// consumer's own policy decides what a base input is.
		baseInputID = "6c1f0d2b8a4e5937c0b1d2e3f4a5968778695a4b3c2d1e0f9a8b7c6d5e4f3a2b"

		// A base image's final tag is content-addressed, not release-scoped.
		tag = baseInputID + "-" + flavor
	)

	for _, forge := range forgesClaiming(t, alwaysValidatesTokens, "an OCI registry") {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()

			target := livetest.Accept(t, forge)

			registry, err := livetest.RegistryHost(target)
			if err != nil {
				t.Fatal(err)
			}

			repo := livetest.NewScratchRepo(t, target, "base-sign")
			imagePath := registry + "/" + target.Owner + "/" + repo

			work := t.TempDir()
			privateKey, publicKey := livetest.CosignKey(t, work)

			pushed := livetest.PushImage(t, target, repo, tag)
			digestRef := imagePath + "@" + pushed.Digest

			// Both verbs take the same description of where the base images
			// live and log in to the registry themselves, so neither is handed
			// an auth file: the login is part of what is under test.
			location := []string{
				"--server-url", target.BaseURL(),
				"--repository", target.Owner + "/" + repo,
				"--expected-repository", imagePath,
				"--registry", registry,
				"--registry-username", target.CredentialUsername,
				"--registry-password-file", writeToken(t, target.Token),
			}

			// The builder identity is the runner's own, so the signer needs the
			// run context a pipeline would have supplied. The source commit and
			// the workflow are flags, which is the point of the verb.
			signEnv := livetest.RunContextEnv(target, provenanceCommit, "main", provenanceRunID)
			signEnv["COSIGN_PASSWORD"] = ""

			sign := livetest.CLIIn(t, target, repo, livetest.RunOptions{Dir: work, Env: signEnv},
				slices.Concat([]string{"container", "base-images", "sign"}, location, []string{
					"--ledger", writeBaseLedger(t, work, map[string]string{
						"role":          "base",
						"image_kind":    "base",
						"flavor":        flavor,
						"ref":           digestRef,
						"digest":        pushed.Digest,
						"final_tag":     imagePath + ":" + tag,
						"base_input_id": baseInputID,
					}),
					"--workflow", workflow,
					"--source-sha", provenanceCommit,
					"--started-on", startedOn,
					"--method", "kms", "--key", privateKey,
				})...,
			)
			if sign.ExitCode != 0 {
				t.Fatalf("%s base-images sign exited %d\nstderr: %s", forge, sign.ExitCode, sign.Stderr)
			}

			image, err := json.Marshal([]map[string]string{{
				"flavor":        flavor,
				"tag":           imagePath + ":" + tag,
				"ref":           digestRef,
				"base_input_id": baseInputID,
			}})
			if err != nil {
				t.Fatal(err)
			}

			// The verifier pins the public key by digest and wants its path
			// relative, as a file in the consumer's checkout would be.
			verify := func(expectedWorkflow string) livetest.Run {
				return livetest.CLIIn(t, target, repo, livetest.RunOptions{Dir: work},
					slices.Concat([]string{"container", "base-images", "promote"}, location, []string{
						"--all-images-json", string(image),
						"--expected-source", target.BaseURL() + "/" + target.Owner + "/" + repo,
						"--expected-workflow", expectedWorkflow,
						"--cosign-public-key-path", filepath.Base(publicKey),
						"--cosign-public-key-sha256", fileSHA256(t, publicKey),
					})...,
				)
			}

			if accepted := verify(workflow); accepted.ExitCode != 0 {
				t.Fatalf("%s: base-images promote refused what base-images sign wrote for %s (exit %d)\nstderr: %s",
					forge, digestRef, accepted.ExitCode, accepted.Stderr)
			}

			// The control: the same image, key and source, and a workflow the
			// signer did not name. A verifier that still accepted it would not
			// be reading the lineage the signer attested.
			refused := verify("some-other-workflow.yml")
			if refused.ExitCode == 0 {
				t.Fatalf("%s: base-images promote accepted %s for a workflow that did not sign it", forge, digestRef)
			}

			if !strings.Contains(refused.Stderr, "failed to verify base image signature and lineage") {
				t.Errorf("%s: promote refused the other workflow for an unrelated reason (exit %d)\nstderr: %s",
					forge, refused.ExitCode, refused.Stderr)
			}

			if livetest.SignaturePublishedToTransparencyLog(t, target, repo, pushed.Digest) {
				t.Errorf("%s: the signature carries a transparency-log entry — it reached the public Rekor log, "+
					"which is append-only and cannot be withdrawn", forge)
			}
		})
	}
}

// writeBaseLedger writes a one-entry base-image ledger and returns its name.
// The entry names a premade SBOM pinned by digest, as a base build's does, so
// the signer attests that document instead of scanning the image for a new one.
func writeBaseLedger(t *testing.T, dir string, entry map[string]string) string {
	t.Helper()

	entry["sbom"] = writeSBOM(t, dir, entry["flavor"])
	entry["sbom_sha256"] = fileSHA256(t, filepath.Join(dir, entry["sbom"]))

	body, err := json.Marshal([]map[string]string{entry})
	if err != nil {
		t.Fatal(err)
	}

	name := "base-images.ledger.json"
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}

	return name
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(body)

	return hex.EncodeToString(sum[:])
}
