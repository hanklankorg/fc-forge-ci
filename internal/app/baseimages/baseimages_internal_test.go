// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
)

func TestVerifyExistingBaseImages_ReportsVerifiedAndMissing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := "forge.example/examplescope/myapp-base"
	baseID := strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("1", 64)
	registry := &fakeBaseImageRegistry{digests: map[string]string{
		repo + ":" + baseID + "-rust": digest,
	}}
	verifier := &fakeBaseImageVerifier{payload: baseLineagePayload(t,
		lineageFields{source: "https://forge.example/examplescope/myapp", workflow: "container-bases.yml", flavor: "rust", baseInputID: baseID},
	)}

	result, err := VerifyExistingBaseImages(ctx, registry, verifier, io.Discard, BaseImageVerifyExistingInput{
		Flavors:            []string{"rust", "go"},
		BaseInputID:        baseID,
		ExpectedRepository: repo,
		ExpectedSource:     "https://forge.example/examplescope/myapp",
		ExpectedWorkflow:   "container-bases.yml",
		CosignPublicKey:    "cosign.pub",
	})
	if err != nil {
		t.Fatalf("VerifyExistingBaseImages() error = %v", err)
	}

	if result.AllFound {
		t.Fatal("AllFound = true, want false")
	}

	wantImages := `[{"flavor":"rust","tag":"forge.example/examplescope/myapp-base:` + baseID + `-rust","ref":"forge.example/examplescope/myapp-base@` + digest + `","candidate_tag":"","candidate_ref":"","base_input_id":"` + baseID + `"}]`
	if result.AllImagesJSON != wantImages {
		t.Errorf("AllImagesJSON = %s, want %s", result.AllImagesJSON, wantImages)
	}

	if result.MissingFlavorsJSON != `["go"]` {
		t.Errorf("MissingFlavorsJSON = %s, want [\"go\"]", result.MissingFlavorsJSON)
	}
}

// A reused base is a parent only when its reads were authenticated, and then
// as the digest its evidence verified on. A missing flavor is no parent.
func TestVerifyExistingBaseImages_ProjectsAuthenticatedParents(t *testing.T) {
	t.Parallel()

	repo := "forge.example/examplescope/myapp-base"
	baseID := strings.Repeat("a", 64)
	ref := repo + "@sha256:" + strings.Repeat("1", 64)

	for _, tc := range []struct {
		name          string
		authenticated bool
		want          string
	}{
		{name: "authenticated", authenticated: true, want: `{"rust":{"ref":"` + ref + `","input_id":"` + baseID + `"}}`},
		{name: "anonymous", want: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			registry := &fakeBaseImageRegistry{digests: map[string]string{repo + ":" + baseID + "-rust": "sha256:" + strings.Repeat("1", 64)}}
			verifier := &fakeBaseImageVerifier{payload: baseLineagePayload(t,
				lineageFields{source: "https://forge.example/examplescope/myapp", workflow: "container-bases.yml", flavor: "rust", baseInputID: baseID},
			)}

			result, err := VerifyExistingBaseImages(t.Context(), registry, verifier, io.Discard, BaseImageVerifyExistingInput{
				Authenticated: tc.authenticated, Flavors: []string{"rust", "go"}, BaseInputID: baseID, ExpectedRepository: repo,
				ExpectedSource: "https://forge.example/examplescope/myapp", ExpectedWorkflow: "container-bases.yml", CosignPublicKey: "cosign.pub",
			})
			if err != nil || result.ParentBasesJSON != tc.want {
				t.Fatalf("ParentBasesJSON = %s, err = %v; want %s", result.ParentBasesJSON, err, tc.want)
			}
		})
	}
}

// GHCR answers an anonymous read of a package that does not exist yet, or is
// private, with DENIED. With no credentials that is a miss
// the first release builds, with a note naming the explicit auth option; a real
// failure, such as an unreachable registry, still stops the check.
func TestVerifyExistingBaseImages_AnAnonymousRefusalIsAMiss(t *testing.T) {
	t.Parallel()

	repo := "ghcr.io/acme/app-base"
	baseID := strings.Repeat("a", 64)
	input := BaseImageVerifyExistingInput{
		Flavors:            []string{"go"},
		BaseInputID:        baseID,
		ExpectedRepository: repo,
		ExpectedSource:     "https://forge.example/acme/app",
		ExpectedWorkflow:   "container-bases.yml",
		CosignPublicKey:    "cosign.pub",
	}
	tag := repo + ":" + baseID + "-go"

	var out bytes.Buffer

	denied := &fakeBaseImageRegistry{resolveErr: map[string]error{tag: fmt.Errorf("token DENIED: %w", errs.ErrPermissionDenied)}}

	result, err := VerifyExistingBaseImages(t.Context(), denied, &fakeBaseImageVerifier{}, &out, input)
	if err != nil || result.AllFound || result.MissingFlavorsJSON != `["go"]` {
		t.Fatalf("result = %+v, err = %v; want go reported missing", result, err)
	}

	if !strings.Contains(out.String(), "--auth-file") || !strings.Contains(out.String(), repo) {
		t.Errorf("the note does not explain private registry authentication:\n%s", out.String())
	}

	unavailable := &fakeBaseImageRegistry{resolveErr: map[string]error{tag: fmt.Errorf("connection refused: %w", errs.ErrDependencyUnavailable)}}
	if _, err := VerifyExistingBaseImages(t.Context(), unavailable, &fakeBaseImageVerifier{}, io.Discard, input); !errors.Is(err, errs.ErrDependencyUnavailable) {
		t.Fatalf("err = %v, want an unreachable registry to stop the check", err)
	}
}

// TestBaseLineageSource_MatchesBothSignerGenerations pins the signer-flip
// compatibility contract: one EXPECTED_SOURCE (the bare repository URL)
// must verify base images signed by the retired `base-images sign` engine
// (externalParameters.source = URL) AND images signed via `container
// ledger sign` over a `release provenance` base predicate
// (externalParameters.source = "git+" + URL, the SLSA VCS URI). Any other
// source, and the git+ form of a DIFFERENT repository, still fail.
func TestBaseLineageSource_MatchesBothSignerGenerations(t *testing.T) {
	t.Parallel()

	baseID := strings.Repeat("a", 64)
	expected := baseImageLineageExpectation{
		Source:      "https://forge.example/examplescope/myapp",
		Workflow:    "container-bases.yml",
		Flavor:      "rust",
		BaseInputID: baseID,
	}

	cases := []struct {
		name   string
		source string
		wantOK bool
	}{
		{"retired signer bare URL", "https://forge.example/examplescope/myapp", true},
		{"ledger sign SLSA git+ URI", "git+https://forge.example/examplescope/myapp", true},
		{"different repository", "https://forge.example/attacker/nanolinter", false},
		{"git+ different repository", "git+https://forge.example/attacker/nanolinter", false},
		{"double git+ prefix", "git+git+https://forge.example/examplescope/myapp", false},
		{"empty source", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := baseLineagePayload(t, lineageFields{
				source: tc.source, workflow: "container-bases.yml", flavor: "rust", baseInputID: baseID,
			})

			err := baseLineageAttestationMatches(payload, expected)
			if tc.wantOK && err != nil {
				t.Fatalf("baseLineageAttestationMatches(source=%q) = %v, want match", tc.source, err)
			}

			if !tc.wantOK && err == nil {
				t.Fatalf("baseLineageAttestationMatches(source=%q) matched, want mismatch", tc.source)
			}
		})
	}
}

func TestPromoteBaseImages_CopiesMissingFinalTagAndEmitsOutputs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := "forge.example/examplescope/myapp-base"
	baseID := strings.Repeat("b", 64)
	rustDigest := "sha256:" + strings.Repeat("2", 64)
	goDigest := "sha256:" + strings.Repeat("3", 64)
	rustFinal := repo + ":" + baseID + "-rust"
	rustCandidate := repo + ":candidate-" + baseID + "-rust"
	goFinal := repo + ":" + baseID + "-go"
	registry := &fakeBaseImageRegistry{digests: map[string]string{
		rustCandidate: rustDigest,
		goFinal:       goDigest,
	}}
	verifier := &fakeBaseImageVerifier{payload: baseLineagePayload(t,
		lineageFields{source: "https://forge.example/examplescope/myapp", workflow: "container-bases.yml", flavor: "rust", baseInputID: baseID},
		lineageFields{source: "https://forge.example/examplescope/myapp", workflow: "container-bases.yml", flavor: "go", baseInputID: baseID},
	)}

	result, err := PromoteBaseImages(ctx, registry, verifier, io.Discard, BaseImagePromoteInput{
		Images: []BaseImageMetadata{
			{Flavor: "rust", Tag: rustFinal, Ref: repo + "@" + rustDigest, CandidateTag: rustCandidate, CandidateRef: repo + "@" + rustDigest, BaseInputID: baseID},
			{Flavor: "go", Tag: goFinal, Ref: repo + "@" + goDigest, BaseInputID: baseID},
		},
		BaseInputs:         []BaseInput{{Flavor: "rust", BaseInputID: baseID}, {Flavor: "go", BaseInputID: baseID}},
		ExpectedRepository: repo,
		ExpectedSource:     "https://forge.example/examplescope/myapp",
		ExpectedWorkflow:   "container-bases.yml",
		CosignPublicKey:    "cosign.pub",
	})
	if err != nil {
		t.Fatalf("PromoteBaseImages() error = %v", err)
	}

	if got := registry.digests[rustFinal]; got != rustDigest {
		t.Fatalf("promoted final digest = %q, want %q", got, rustDigest)
	}

	if result.BaseInputID != baseID {
		t.Errorf("BaseInputID = %q, want %q", result.BaseInputID, baseID)
	}

	wantImages := `[{"flavor":"go","ref":"forge.example/examplescope/myapp-base@` + goDigest + `","base_input_id":"` + baseID + `"},{"flavor":"rust","ref":"forge.example/examplescope/myapp-base@` + rustDigest + `","base_input_id":"` + baseID + `"}]`
	if result.BaseImagesJSON != wantImages {
		t.Errorf("BaseImagesJSON = %s, want %s", result.BaseImagesJSON, wantImages)
	}

	wantInputs := `[{"flavor":"go","base_input_id":"` + baseID + `"},{"flavor":"rust","base_input_id":"` + baseID + `"}]`
	if result.BaseInputIDsJSON != wantInputs {
		t.Errorf("BaseInputIDsJSON = %s, want %s", result.BaseInputIDsJSON, wantInputs)
	}
}

func TestCleanupStagingBaseImages_DeletesPromotedAndStaleTags(t *testing.T) {
	t.Parallel()

	repo := "forge.example/examplescope/myapp-base"
	baseID := strings.Repeat("1", 64)
	contentID := strings.Repeat("2", 64)
	olderID := strings.Repeat("3", 64)

	// One package holds a root (core) and flavors built on it, each by its own
	// stage, possibly in parallel. Every manifest candidate here is promoted:
	// its final exists with the same digest, which is all the sweep needs to
	// delete it. go and rust-slim are other stages' in-flight candidates, and
	// rust-slim shares rust's name as a prefix. rust-arm64 ends in an
	// architecture, and its promoted amd64 candidate must not pass for rust's.
	digests := map[string]string{
		repo + ":" + baseID + "-rust-arm64":                  "sha256:" + strings.Repeat("d", 64),
		repo + ":staging-" + contentID + "-rust-arm64-amd64": "sha256:" + strings.Repeat("e", 64),
		repo + ":staging-" + contentID + "-go-" + "amd64":    "sha256:" + strings.Repeat("e", 64),
	}

	for idx, version := range []string{baseID + "-core", baseID + "-rust", baseID + "-go", baseID + "-rust-slim", olderID + "-core", olderID + "-rust"} {
		digest := fmt.Sprintf("sha256:%064x", idx+1)
		digests[repo+":"+version] = digest
		digests[repo+":staging-"+version] = digest
	}

	for _, flavor := range []string{"core", "rust"} {
		digests[repo+":staging-"+contentID+"-"+flavor+"-amd64"] = "sha256:" + strings.Repeat("e", 64)
	}

	image := func(flavor string) BaseImageMetadata {
		return BaseImageMetadata{
			Flavor:       flavor,
			Tag:          repo + ":" + baseID + "-" + flavor,
			Ref:          repo + "@" + digests[repo+":"+baseID+"-"+flavor],
			CandidateTag: repo + ":staging-" + baseID + "-" + flavor,
			CandidateRef: repo + "@" + digests[repo+":"+baseID+"-"+flavor],
			BaseInputID:  baseID,
		}
	}

	staging := func(version string) string { return repo + ":staging-" + version }

	for _, tc := range []struct {
		name    string
		flavors []string
		// inputs, when set, is the base inputs' flavors; otherwise they are
		// the images' own.
		inputs []string
		// gone means the images' candidates are already removed.
		gone bool
		// The order is the pass order: the promoted candidates first, then
		// the given flavors' stale versions in sorted listing order.
		wantDeleted []string
		wantLog     string
	}{
		{
			name:        "flavor stage keeps the root's and other flavors' candidates",
			flavors:     []string{"rust"},
			wantDeleted: []string{staging(baseID + "-rust"), staging(contentID + "-rust-amd64"), staging(olderID + "-rust")},
		},
		{
			name:        "root stage keeps every flavor's candidate",
			flavors:     []string{"core"},
			wantDeleted: []string{staging(baseID + "-core"), staging(contentID + "-core-amd64"), staging(olderID + "-core")},
		},
		{
			name:    "one cleanup for several flavors sweeps each of them",
			flavors: []string{"core", "rust"},
			wantDeleted: []string{
				staging(baseID + "-core"), staging(baseID + "-rust"),
				staging(contentID + "-core-amd64"), staging(contentID + "-rust-amd64"),
				staging(olderID + "-core"), staging(olderID + "-rust"),
			},
		},
		{
			name:        "graph-wide base inputs do not widen the sweep",
			flavors:     []string{"rust"},
			inputs:      []string{"core", "go", "rust", "rust-arm64", "rust-slim"},
			wantDeleted: []string{staging(baseID + "-rust"), staging(contentID + "-rust-amd64"), staging(olderID + "-rust")},
		},
		{
			name:        "own candidate already removed",
			flavors:     []string{"rust"},
			gone:        true,
			wantDeleted: []string{staging(contentID + "-rust-amd64"), staging(olderID + "-rust")},
			wantLog:     "Staging base tag already removed: " + staging(baseID+"-rust"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := BaseImageCleanupStagingInput{ExpectedRepository: repo}
			registry := &fakeBaseImageRegistry{digests: maps.Clone(digests)}
			own := map[string]bool{}

			for _, flavor := range tc.flavors {
				own[image(flavor).CandidateTag] = true
				in.Images = append(in.Images, image(flavor))

				if tc.gone {
					delete(registry.digests, image(flavor).CandidateTag)
				}
			}

			inputs := tc.inputs
			if inputs == nil {
				inputs = tc.flavors
			}

			for _, flavor := range inputs {
				in.BaseInputs = append(in.BaseInputs, BaseInput{Flavor: flavor, ContentID: contentID, BaseInputID: baseID})
			}

			// The listing holds every staging version except the candidates
			// this cleanup deletes first, as the registry would after them.
			cleaner := &fakeBaseImagePackageAPI{versions: []string{"1.0.0"}}

			for ref := range digests {
				if version := tagName(ref); strings.HasPrefix(version, "staging-") && !own[ref] {
					cleaner.versions = append(cleaner.versions, version)
				}
			}

			var log bytes.Buffer

			if err := CleanupStagingBaseImages(context.Background(), registry, cleaner, &log, in); err != nil {
				t.Fatalf("CleanupStagingBaseImages() error = %v", err)
			}

			// Ordered equality, not membership. Deletions here are
			// irreversible, and a substring search over the joined list cannot
			// see the two failures that matter: a tag deleted that should have
			// been kept -- a final tag, or another flavor's staging -- and the
			// same tag deleted twice, which a registry answers with a 404 the
			// second time.
			if !slices.Equal(cleaner.deleted, tc.wantDeleted) {
				t.Fatalf("deleted = %q\nwant     %q", cleaner.deleted, tc.wantDeleted)
			}

			// Every delete that was attempted also landed: a refused one would
			// leave deleteHit ahead of the recorded list, and the pass would
			// have reported success having skipped it.
			if cleaner.deleteHit != len(tc.wantDeleted) {
				t.Errorf("DeleteTag was called %d time(s) but %d landed", cleaner.deleteHit, len(tc.wantDeleted))
			}

			if cleaner.listOwner != "examplescope" || cleaner.listName != "myapp-base" {
				t.Fatalf("list package owner/name = %s/%s", cleaner.listOwner, cleaner.listName)
			}

			if !strings.Contains(log.String(), "Checking for stale staging base tags of "+strings.Join(tc.flavors, ", ")+".") ||
				!strings.Contains(log.String(), "Preserving base signature artifacts") {
				t.Fatalf("log missing sweep scope or signature preservation message: %s", log.String())
			}

			if !strings.Contains(log.String(), tc.wantLog) {
				t.Fatalf("log missing %q: %s", tc.wantLog, log.String())
			}
		})
	}
}

func TestCleanupStagingBaseImages_PreservesReusableArchUntilFinalExists(t *testing.T) {
	t.Parallel()

	repo := "forge.example/examplescope/myapp-base"
	baseID := strings.Repeat("4", 64)
	contentID := strings.Repeat("5", 64)
	cleaner := &fakeBaseImagePackageAPI{versions: []string{"staging-" + contentID + "-go-arm64"}}

	var log bytes.Buffer

	// The image has no candidate of its own; it gives the sweep its flavor.
	err := CleanupStagingBaseImages(context.Background(), &fakeBaseImageRegistry{digests: map[string]string{}}, cleaner, &log, BaseImageCleanupStagingInput{
		Images:             []BaseImageMetadata{{Flavor: "go", Tag: repo + ":" + baseID + "-go", Ref: repo + "@sha256:" + strings.Repeat("6", 64), BaseInputID: baseID}},
		BaseInputs:         []BaseInput{{Flavor: "go", ContentID: contentID, BaseInputID: baseID}},
		ExpectedRepository: repo,
	})
	if err != nil {
		t.Fatalf("CleanupStagingBaseImages() error = %v", err)
	}

	if len(cleaner.deleted) != 0 {
		t.Fatalf("deleted arch tag before final existed: %v", cleaner.deleted)
	}

	if !strings.Contains(log.String(), "Preserving reusable architecture base tag until final exists") {
		t.Fatalf("log missing preserve message: %s", log.String())
	}
}

func TestCleanupStagingBaseImages_RejectsUnexpectedStagingVersion(t *testing.T) {
	t.Parallel()

	repo := "forge.example/examplescope/myapp-base"
	baseID := strings.Repeat("4", 64)
	digest := "sha256:" + strings.Repeat("6", 64)

	for _, tc := range []struct {
		name    string
		image   BaseImageMetadata
		version string
		wantErr string
	}{
		{
			// The version list comes back from the forge API, so a name that
			// is not a staging version is either a real release or something
			// this command has no business removing, whichever flavor it names.
			name:    "listed version outside the staging policy",
			image:   BaseImageMetadata{Flavor: "go", Tag: repo + ":" + baseID + "-go", Ref: repo + "@" + digest, BaseInputID: baseID},
			version: "staging-not-a-content-id-rust",
			wantErr: "refusing to delete unexpected staging version",
		},
		{
			name: "entry carrying another flavor's candidate",
			image: BaseImageMetadata{
				Flavor: "rust", Tag: repo + ":" + baseID + "-rust", Ref: repo + "@" + digest,
				CandidateTag: repo + ":staging-" + baseID + "-go", CandidateRef: repo + "@" + digest, BaseInputID: baseID,
			},
			version: "staging-" + baseID + "-go",
			wantErr: "refusing to delete unexpected staging tag",
		},
		{
			name: "entry naming another flavor's final tag",
			image: BaseImageMetadata{
				Flavor: "rust", Tag: repo + ":" + baseID + "-go", Ref: repo + "@" + digest,
				CandidateTag: repo + ":staging-" + baseID + "-rust", CandidateRef: repo + "@" + digest, BaseInputID: baseID,
			},
			version: "staging-" + baseID + "-rust",
			wantErr: "refusing cleanup with unexpected final tag",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			registry := &fakeBaseImageRegistry{digests: map[string]string{repo + ":" + baseID + "-rust": digest, repo + ":" + baseID + "-go": digest, repo + ":" + tc.version: digest}}
			cleaner := &fakeBaseImagePackageAPI{versions: []string{tc.version}}

			err := CleanupStagingBaseImages(context.Background(), registry, cleaner, io.Discard, BaseImageCleanupStagingInput{
				Images:             []BaseImageMetadata{tc.image},
				ExpectedRepository: repo,
			})
			if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want ErrValidation with %q", err, tc.wantErr)
			}

			if len(cleaner.deleted) != 0 {
				t.Fatalf("deleted = %v, want nothing", cleaner.deleted)
			}
		})
	}
}

func TestCleanupStagingBaseImages_FailsWhenFinalDigestDiffers(t *testing.T) {
	t.Parallel()

	repo := "forge.example/examplescope/myapp-base"
	baseID := strings.Repeat("9", 64)
	digest := "sha256:" + strings.Repeat("a", 64)
	registry := &fakeBaseImageRegistry{digests: map[string]string{repo + ":" + baseID + "-rust": "sha256:" + strings.Repeat("b", 64)}}
	cleaner := &fakeBaseImagePackageAPI{}

	err := CleanupStagingBaseImages(context.Background(), registry, cleaner, io.Discard, BaseImageCleanupStagingInput{
		Images: []BaseImageMetadata{{
			Flavor:       "rust",
			Tag:          repo + ":" + baseID + "-rust",
			Ref:          repo + "@" + digest,
			CandidateTag: repo + ":staging-" + baseID + "-rust",
			CandidateRef: repo + "@" + digest,
			BaseInputID:  baseID,
		}},
		ExpectedRepository: repo,
	})
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "final base tag does not point to the promoted digest") {
		t.Fatalf("err = %v, want cleanup safety failure", err)
	}

	if len(cleaner.deleted) != 0 {
		t.Fatalf("deleted despite final digest mismatch: %v", cleaner.deleted)
	}
}

type fakeBaseImageRegistry struct {
	digests    map[string]string
	resolveErr map[string]error
	copied     []string
	resolved   []string
}

func (f *fakeBaseImageRegistry) ResolveDigest(_ context.Context, ref string) (string, error) {
	f.resolved = append(f.resolved, ref)
	if err := f.resolveErr[ref]; err != nil {
		return "", err
	}

	digest, ok := f.digests[ref]
	if !ok {
		return "", fmt.Errorf("missing %s: %w", ref, errs.ErrMissingInput)
	}

	return digest, nil
}

func (f *fakeBaseImageRegistry) CopyTag(_ context.Context, source, dest string) error {
	f.copied = append(f.copied, source+"->"+dest)

	// Split here rather than calling the production digestFromRef: a fake that
	// borrows the helper it is standing in for agrees with it by construction,
	// so a wrong assumption would be wrong in both places and the test would
	// still pass. docs/live-runs.md names this as the reason the live tier
	// exists; it costs one line to not reproduce it here.
	_, digest, _ := strings.Cut(source, "@")
	f.digests[dest] = digest

	return nil
}

type fakeBaseImagePackageAPI struct {
	versions  []string
	deleted   []string
	listOwner string
	listName  string
	deleteErr error
	// failOn, when non-zero, fails only that call (1-based) and lets the
	// others through. deleteErr alone fails every deletion, which cannot
	// express a failure partway through a prune pass.
	failOn    int
	deleteHit int
}

func (f *fakeBaseImagePackageAPI) DeleteTag(_ context.Context, ref string) error {
	f.deleteHit++

	if f.deleteErr != nil && (f.failOn == 0 || f.failOn == f.deleteHit) {
		return f.deleteErr
	}

	f.deleted = append(f.deleted, ref)

	return nil
}

func (f *fakeBaseImagePackageAPI) ListContainerPackageVersions(_ context.Context, owner, name string) ([]string, error) {
	f.listOwner = owner
	f.listName = name

	return append([]string{}, f.versions...), nil
}

type fakeBaseImageVerifier struct {
	payload []byte
}

func (f *fakeBaseImageVerifier) VerifyImage(context.Context, cosign.VerifyImageInput, io.Writer) error {
	return nil
}

func (f *fakeBaseImageVerifier) VerifyAttestation(context.Context, cosign.VerifyAttestationInput, io.Writer) error {
	return nil
}

func (f *fakeBaseImageVerifier) VerifyAttestationOutput(_ context.Context, _ cosign.VerifyAttestationInput, out, _ io.Writer) error {
	_, err := out.Write(f.payload)

	return err
}

type lineageFields struct {
	source      string
	workflow    string
	flavor      string
	baseInputID string
}

func baseLineagePayload(t *testing.T, fields ...lineageFields) []byte {
	t.Helper()

	envelopes := make([]map[string]string, 0, len(fields))
	for _, field := range fields {
		statement := map[string]any{
			"predicateType": provenance.PredicateTypeV1,
			"predicate": map[string]any{
				"buildDefinition": map[string]any{
					"externalParameters": map[string]any{
						"source":        field.source,
						"workflow":      field.workflow,
						"flavor":        field.flavor,
						"base_input_id": field.baseInputID,
					},
				},
			},
		}

		statementBody, err := json.Marshal(statement)
		if err != nil {
			t.Fatalf("marshal statement: %v", err)
		}

		envelopes = append(envelopes, map[string]string{"payload": base64.StdEncoding.EncodeToString(statementBody)})
	}

	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(envelopes); err != nil {
		t.Fatalf("marshal envelopes: %v", err)
	}

	return body.Bytes()
}

// An absent final must preserve the candidate through the stale sweep too.
func TestCleanupStagingBaseImages_AbsentFinalTagBlocksDeletion(t *testing.T) {
	t.Parallel()

	const repo = "forge.example/examplescope/myapp-base"

	baseID := strings.Repeat("1", 64)
	digest := "sha256:" + strings.Repeat("6", 64)
	image := BaseImageMetadata{
		Flavor:       "rust",
		Tag:          repo + ":" + baseID + "-rust",
		Ref:          repo + "@" + digest,
		CandidateTag: repo + ":staging-" + baseID + "-rust",
		CandidateRef: repo + "@" + digest,
		BaseInputID:  baseID,
	}

	// Only the candidate exists; the final tag it should have been promoted
	// to is absent.
	registry := &fakeBaseImageRegistry{digests: map[string]string{
		image.CandidateTag: digest,
	}}
	cleaner := &fakeBaseImagePackageAPI{versions: []string{tagName(image.CandidateTag)}}

	var log bytes.Buffer

	err := CleanupStagingBaseImages(context.Background(), registry, cleaner, &log, BaseImageCleanupStagingInput{
		Images:             []BaseImageMetadata{image},
		BaseInputs:         []BaseInput{{Flavor: "rust", ContentID: strings.Repeat("2", 64), BaseInputID: baseID}},
		ExpectedRepository: repo,
	})

	if !errors.Is(err, errs.ErrMissingInput) || log.Len() != 0 || cleaner.listName != "" {
		t.Fatalf("err=%v output=%s list=%s", err, &log, cleaner.listName)
	}

	if strings.Contains(strings.Join(cleaner.deleted, ","), image.CandidateTag) {
		t.Errorf("deleted the candidate tag despite the safety check: %v", cleaner.deleted)
	}
}
