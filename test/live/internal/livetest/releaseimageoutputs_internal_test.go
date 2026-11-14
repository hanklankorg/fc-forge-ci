// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
)

func releaseOutputFixture(t *testing.T) (ReleaseImageOutputs, []imageledger.Entry) {
	t.Helper()

	file := filepath.Join(t.TempDir(), "artifacts.yml")
	if err := os.WriteFile(file, []byte("containers:\n  - name: app\n    platforms: linux/amd64\n  - name: minimal\n    platforms: linux/amd64\n    tag-suffix: -minimal\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	in := ReleaseImageOutputs{Repository: "registry.example.invalid/owner/app", Tag: "v0.3.0", Commit: strings.Repeat("a", 64), Source: "https://forge.example.invalid/owner/app", Workflow: ".forgejo/workflows/release.yml", PublicKey: "/inert/public.pem"}

	plan, err := appconfig.ResolveReleaseImagesPlan(appconfig.EmitReleaseImagesPlanInput{
		Config: appconfig.EmitConfigPlanInput{Path: file}, Images: pipeline.ReleaseImagesInput{ImageRepository: in.Repository, Repository: "owner/app", ReleaseTag: in.Tag},
	}, output.Annotator{})
	if err != nil {
		t.Fatal(err)
	}

	in.Plan = plan
	entries := make([]imageledger.Entry, 0, len(plan.Images))

	for index, image := range plan.Images {
		entry := imageledger.Entry{Role: image.Name, ImageKind: imageledger.ImageKindRelease, FinalTag: in.Repository + ":" + image.FinalTagName, CandidateTag: in.Repository + ":" + image.CandidateTag}
		entries = append(entries, entry.PinDigest("sha256:"+strings.Repeat([]string{"b", "c"}[index], 64)))
	}

	in.Ledger, err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}

	return in, entries
}

type releaseOutputEvidence struct {
	expected                             ReleaseImageOutputs
	errSignature, errSBOM, errProvenance error
	requests                             []string
	attestations                         []container.AttestationVerifyRequest
}

func (f *releaseOutputEvidence) VerifyImage(_ context.Context, in container.ImageVerifyRequest, _ io.Writer) error {
	f.requests = append(f.requests, in.ImageRef)
	if in.KeyRef != f.expected.PublicKey {
		return errors.New("wrong trust key")
	}

	// This fake models the authenticated signature set. A forged receipt and
	// matching registry tag cannot mint a signature for another digest.
	entries, err := imageledger.Parse(f.expected.Ledger)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if in.ImageRef == entry.Ref {
			return f.errSignature
		}
	}

	return errors.New("digest is not signed")
}

func (f *releaseOutputEvidence) VerifyAttestation(_ context.Context, in container.AttestationVerifyRequest, _ io.Writer) error {
	f.attestations = append(f.attestations, in)
	if len(f.requests) == 0 || in.ImageRef != f.requests[len(f.requests)-1] {
		return errors.New("SBOM request changed the authenticated image digest")
	}

	if in.PredicateType != container.PredicateTypeCycloneDX || in.KeyRef != f.expected.PublicKey {
		return errors.New("wrong SBOM request")
	}

	return f.errSBOM
}

func (f *releaseOutputEvidence) VerifyAttestationOutput(_ context.Context, in container.AttestationVerifyRequest, out, _ io.Writer) error {
	f.attestations = append(f.attestations, in)
	if len(f.requests) == 0 || in.ImageRef != f.requests[len(f.requests)-1] {
		return errors.New("provenance request changed the authenticated image digest")
	}

	if in.PredicateType != container.PredicateTypeSLSAProvenance1 || in.KeyRef != f.expected.PublicKey {
		return errors.New("wrong provenance request")
	}

	statement := map[string]any{
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate": map[string]any{"buildDefinition": map[string]any{
			"externalParameters": map[string]any{"workflow": map[string]string{"ref": f.expected.Tag, "repository": f.expected.Source, "path": f.expected.Workflow}},
			"resolvedDependencies": []any{map[string]any{
				"uri":    provenance.SourceDependency(f.expected.Source, f.expected.Tag, f.expected.Commit).URI,
				"digest": map[string]string{"gitCommit": f.expected.Commit},
			}},
		}},
	}

	body, err := json.Marshal(statement)
	if err != nil {
		return err
	}

	if err := json.NewEncoder(out).Encode(map[string]string{"payload": base64.StdEncoding.EncodeToString(body)}); err != nil {
		return err
	}
	// Even plausible output accompanied by a failed verification is refused.
	return f.errProvenance
}

func TestReleaseImageOutputsReadback(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"matching", "wrong source", "wrong commit", "wrong tag", "wrong workflow", "wrong key", "wrong signature", "missing SBOM", "invalid provenance signature", "wrong digest", "coordinated digest swap", "missing final tag"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in, entries := releaseOutputFixture(t)
			verifier := &releaseOutputEvidence{expected: in}
			failure := errors.New("inert evidence failure")

			switch name {
			case "wrong source":
				verifier.expected.Source += "-other"
			case "wrong commit":
				verifier.expected.Commit = strings.Repeat("c", 64)
			case "wrong tag":
				verifier.expected.Tag = "v0.4.0"
			case "wrong workflow":
				verifier.expected.Workflow = ".forgejo/workflows/other.yml"
			case "wrong key":
				in.PublicKey = "/inert/other.pem"
			case "coordinated digest swap":
				entries[0] = entries[0].PinDigest("sha256:" + strings.Repeat("d", 64))

				var err error

				in.Ledger, err = json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
			case "wrong signature":
				verifier.errSignature = failure
			case "missing SBOM":
				verifier.errSBOM = failure
			case "invalid provenance signature":
				verifier.errProvenance = failure
			}

			reads := 0
			resolve := func(_ context.Context, tag string) (string, error) {
				reads++
				if tag != entries[reads-1].FinalTag {
					t.Fatalf("unexpected registry read %s", tag)
				}

				if name == "missing final tag" {
					return "", failure
				}

				if name == "wrong digest" {
					return "sha256:" + strings.Repeat("d", 64), nil
				}

				return entries[reads-1].Digest, nil
			}

			err := VerifyReleaseImageOutputs(t.Context(), in, resolve, verifier, io.Discard)
			if name == "matching" {
				if err != nil || reads != 2 || len(verifier.requests) != 2 || len(verifier.attestations) != 4 {
					t.Fatalf("readback incomplete: reads=%d evidence=%v err=%v", reads, verifier.requests, err)
				}

				for index, ref := range verifier.requests {
					if ref != in.Repository+"@"+entries[index].Digest {
						t.Fatalf("evidence did not use exact top-level receipt digest: %s", ref)
					}
				}

				for index, request := range verifier.attestations {
					if request.ImageRef != entries[index/2].Ref {
						t.Fatalf("attestation did not use exact authenticated image digest: %+v", request)
					}
				}
			} else if err == nil {
				t.Fatal("accepted substituted or missing release image evidence")
			}

			if name == "wrong digest" && len(verifier.requests) != 0 {
				t.Fatal("wrong final digest reached evidence verification")
			}
		})
	}
}

func TestReleaseImageOutputsRejectReceiptAuthorityBeforeReads(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"missing output", "empty plan", "extra output", "wrong repository", "wrong tag", "wrong role", "wrong candidate", "missing source", "base scope", "duplicate output", "malformed ledger"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in, entries := releaseOutputFixture(t)

			switch name {
			case "missing output":
				entries = entries[:1]
			case "empty plan":
				in.Plan = pipeline.ReleaseImagesPlan{}
				entries = nil
			case "extra output":
				entries = append(entries, entries[0])
			case "wrong repository":
				entries[0].FinalTag = "registry.example.invalid/foreign/app:" + in.Tag
			case "wrong tag":
				entries[0].FinalTag += "-unplanned"
			case "wrong role":
				entries[1].Role = "other"
			case "wrong candidate":
				entries[0].CandidateTag += "-other"
			case "missing source":
				in.Commit = ""
			case "base scope":
				entries[0].ImageKind = imageledger.ImageKindBase
			case "duplicate output":
				entries[1] = entries[0]
			}

			var err error

			in.Ledger, err = json.Marshal(entries)
			if err != nil {
				t.Fatal(err)
			}

			if name == "malformed ledger" {
				in.Ledger = []byte("not JSON")
			}

			verifier := &releaseOutputEvidence{expected: in}

			err = VerifyReleaseImageOutputs(t.Context(), in, func(context.Context, string) (string, error) {
				t.Fatal("unadmitted receipt reached registry")

				return "", nil
			}, verifier, io.Discard)
			if err == nil || len(verifier.requests) != 0 {
				t.Fatalf("unadmitted receipt reached evidence: %v, %v", verifier.requests, err)
			}
		})
	}
}

// The journal a release kept is held to the release's receipt: the journal
// the engine writes for the receipt's entries has no finding, also where it
// promoted from the digest reference, and one that names another digest,
// lacks an image or names one more has.
func TestPromotionJournalNamesTheReleasedImages(t *testing.T) {
	t.Parallel()

	in, entries := releaseOutputFixture(t)

	records := make([]imageledger.PromotionRecord, 0, len(entries))
	for _, entry := range entries {
		records = append(records, imageledger.PromotionRecord{
			Version: imageledger.PromotionJournalVersion, SourceRef: entry.CandidateTag + "@" + entry.Digest,
			FinalTag: entry.FinalTag, CandidateTag: entry.CandidateTag,
		})
	}

	// The engine falls back to the digest reference where the candidate is gone.
	records[1].SourceRef = entries[1].Ref

	findings := func(records []imageledger.PromotionRecord) []string {
		journal, err := imageledger.MarshalPromotionJournal(records)
		if err != nil {
			t.Fatal(err)
		}

		return PromotionJournalFindings(journal, in.Ledger)
	}

	if got := findings(records); len(got) != 0 {
		t.Fatalf("the journal of the released images has findings: %q", got)
	}

	otherDigest := append([]imageledger.PromotionRecord(nil), records...)
	otherDigest[0].SourceRef = entries[0].CandidateTag + "@sha256:" + strings.Repeat("d", 64)

	unpublished := append(append([]imageledger.PromotionRecord(nil), records...), records[0])
	unpublished[2].FinalTag = in.Repository + ":unpublished"

	for name, changed := range map[string][]imageledger.PromotionRecord{
		"another digest": otherDigest, "an image missing": records[:1], "an image the release did not publish": unpublished,
	} {
		if got := findings(changed); len(got) == 0 {
			t.Errorf("a journal with %s has no finding", name)
		}
	}

	if got := PromotionJournalFindings([]byte(`{"version":1,"registry_password":"x"}`+"\n"), in.Ledger); len(got) != 1 {
		t.Errorf("a journal with a member the engine does not write has findings %q, want it refused", got)
	}
}

func TestReleaseImageOutputSettingsAreExplicitAndReleaseOnly(t *testing.T) {
	t.Parallel()

	for _, event := range []string{TrialPullRequest, TrialLifecycle, TrialSnapshot, TrialRelease} {
		for _, path := range []string{".forge-ci/artifacts.yml", "../outside.yml", "/absolute.yml", ".forge-ci/../artifacts.yml"} {
			_, err := ParseTrialSettings(event, []string{"release-images-config=" + path})

			want := event == TrialRelease && path == ".forge-ci/artifacts.yml"
			if (err == nil) != want {
				t.Fatalf("event=%s path=%s: %v", event, path, err)
			}
		}
	}

	if _, err := ParseTrialSettings(TrialRelease, []string{"release-images-config=a.yml", "release-images-config=b.yml"}); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("duplicate declaration admitted: %v", err)
	}

	if _, err := ParseTrialSettings(TrialRelease, []string{"release-images-config=a.yml", "image=registry.example.invalid/app"}); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("local and external authority mixed: %v", err)
	}
}

// The operator's native image release (docs/live-runs.md, Forgejo
// release images) opts into the readback with these settings.
func TestNativeReleaseSettingsSelectTheImageReadback(t *testing.T) {
	t.Parallel()

	settings, err := ParseTrialSettings(TrialRelease, []string{"version=0.3.0", "release-images-config=.forge-ci/artifacts.yml", "RELEASE_PLATFORMS=linux/amd64"})
	if err != nil || settings.ReleaseImagesConfig != ".forge-ci/artifacts.yml" {
		t.Fatalf("readback was not selected: %+v, %v", settings, err)
	}
}
