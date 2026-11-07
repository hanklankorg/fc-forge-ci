// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appartifact "github.com/diggsweden/forge-ci/internal/app/artifact"
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// Only local OCI inspection uses an adapter. All remote registry operations,
// artifact transport, scanner execution and signing are inert port doubles.
type rootBaseRegistry struct {
	appcontainer.LayoutInspector

	raw     map[string][]byte
	images  map[string]domaincontainer.LayoutImage
	writes  []string
	onWrite func()
}

func (r *rootBaseRegistry) Manifest(_ context.Context, ref string) ([]byte, error) {
	if body, ok := r.raw[ref]; ok {
		return body, nil
	}

	return nil, errs.ErrMissingInput
}

func (r *rootBaseRegistry) ResolveDigest(ctx context.Context, ref string) (string, error) {
	body, err := r.Manifest(ctx, ref)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("sha256:%x", sha256.Sum256(body)), nil
}

func (r *rootBaseRegistry) PushLayout(ctx context.Context, directory, ref string) (string, error) {
	r.onWrite()

	image, err := r.InspectLayout(ctx, directory)
	if err != nil {
		return "", err
	}

	body, err := os.ReadFile(filepath.Join(directory, "blobs", "sha256", strings.TrimPrefix(image.Digest, "sha256:")))
	if err != nil {
		return "", err
	}

	repository := domaincontainer.StripTagOrDigest(ref)
	r.raw[ref], r.raw[repository+"@"+image.Digest] = body, body
	r.images[repository+"@"+image.Digest] = image
	r.writes = append(r.writes, ref)

	return image.Digest, nil
}

func (r *rootBaseRegistry) ImagePlatform(_ context.Context, ref string) (string, string, error) {
	image, ok := r.images[ref]
	if !ok {
		return "", "", errs.ErrMissingInput
	}

	osName, arch, _ := strings.Cut(image.Platform, "/")

	return osName, arch, nil
}

func (r *rootBaseRegistry) Labels(_ context.Context, ref string) (map[string]string, error) {
	return r.images[ref].Labels, nil
}

func (r *rootBaseRegistry) MergeManifestWithDigest(_ context.Context, repository string, digests, tags []string) (string, error) {
	r.onWrite()
	// Assembly checks the served index digest; no registry server is started.
	index := v1.IndexManifest{SchemaVersion: 2, MediaType: types.OCIImageIndex}

	for _, digest := range digests {
		hash, err := v1.NewHash("sha256:" + digest)
		if err != nil {
			return "", err
		}

		ref := repository + "@" + hash.String()
		osName, arch, _ := strings.Cut(r.images[ref].Platform, "/")
		index.Manifests = append(index.Manifests, v1.Descriptor{
			MediaType: types.OCIManifestSchema1, Digest: hash, Size: int64(len(r.raw[ref])),
			Platform: &v1.Platform{OS: osName, Architecture: arch},
		})
	}

	body, err := json.Marshal(index)
	if err != nil {
		return "", err
	}

	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	r.raw[repository+"@"+digest] = body

	for _, tag := range tags {
		r.raw[tag] = body
		r.writes = append(r.writes, tag)
	}

	return digest, nil
}

func (r *rootBaseRegistry) CopyTag(ctx context.Context, source, target string) error {
	body, err := r.Manifest(ctx, source)
	if err != nil {
		return err
	}

	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	r.raw[target] = body
	r.raw[domaincontainer.StripTagOrDigest(target)+"@"+digest] = body
	r.writes = append(r.writes, target)

	return nil
}

type rootBaseFixture struct {
	in       appbaseimages.PublishRootBaseInput
	registry *rootBaseRegistry
	gated    []string
	syft     *rootBaseSyft
}

type rootBaseSyft struct {
	targets []string
	bad     bool
}

func (s *rootBaseSyft) Generate(_ context.Context, target string, outputs map[string]string, _ io.Writer) error {
	s.targets = append(s.targets, target)

	body := fmt.Sprintf("{\n  \"bomFormat\":\"CycloneDX\",\"specVersion\":\"1.6\",\"version\":1,\"serialNumber\":%q\n}\n", target)
	if s.bad {
		body = `{}`
	}

	for _, file := range outputs {
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			return err
		}
	}

	return nil
}

func newRootBaseFixture(t *testing.T) *rootBaseFixture {
	t.Helper()
	t.Chdir(t.TempDir())

	repository := "registry.example/org/base"
	id := strings.Repeat("a", 64)
	image := pipeline.ReleaseImage{
		Name: "root", Role: "base", ImageKind: "base", Flavor: "core", BaseInputID: id,
		ImageRepository: repository, FinalTagName: id + "-core", CandidateTag: "staging-" + id + "-core",
		Archs: "amd64\narm64", Scan: true, RequiredLabels: "org.opencontainers.image.revision=" + strings.Repeat("b", 40),
		ProvenanceJSON: `{"material":"opaque-consumer-identity"}`,
	}
	fixture := &rootBaseFixture{syft: &rootBaseSyft{}}
	fixture.in = appbaseimages.PublishRootBaseInput{
		Plan:               pipeline.ReleaseImagesPlan{Images: []pipeline.ReleaseImage{image}},
		ExpectedRepository: repository, LayoutsDir: "layouts", OutputDir: "payload", AuthFile: "auth.json",
		SourceSHA: strings.Repeat("b", 40), ServerURL: "https://source.example", Repository: "org/app", RetryAttempts: 1,
		Trivy: &fakeImageEvidenceTrivy{}, Syft: fixture.syft,
		Gate: func(_ context.Context, _, platform string) error {
			fixture.gated = append(fixture.gated, platform)

			return nil
		},
	}
	require.NoError(t, os.WriteFile(fixture.in.AuthFile, []byte(`{"auths":{}}`), 0o600))
	fixture.registry = &rootBaseRegistry{LayoutInspector: ociregistry.NewAnonymous(), raw: map[string][]byte{}, images: map[string]domaincontainer.LayoutImage{}, onWrite: func() {
		platforms := make([]string, 0, len(fixture.in.Plan.Builds))
		for arch := range strings.FieldsSeq(fixture.in.Plan.Images[0].Archs) {
			platforms = append(platforms, "linux/"+arch)
		}

		require.Equal(t, platforms, fixture.gated, "complete gate barrier before every upload")
	}}

	for _, arch := range []string{"amd64", "arm64"} {
		tag := repository + ":" + image.CandidateTag + "-" + arch
		dir := filepath.Join(fixture.in.LayoutsDir, "root-arch-"+arch)
		_, staged := stagedLayout(t, t.Context(), filepath.Join(dir, "layout"), tag, arch)
		digest, err := staged.Digest()
		require.NoError(t, err)
		body, err := json.Marshal(appcontainer.SignerImageArchMetadata{Arch: arch, Tag: tag, Digest: digest.String(), Ref: repository + "@" + digest.String()})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "root-"+arch+".json"), body, 0o600))
		fixture.in.Plan.Builds = append(fixture.in.Plan.Builds, pipeline.ReleaseImageBuild{
			Name: "root", Arch: arch, Platform: "linux/" + arch, ContainerFile: "Containerfile", Context: ".",
			ImageRepository: repository, FinalTagName: image.FinalTagName, CandidateTag: image.CandidateTag,
		})
	}

	return fixture
}

func (f *rootBaseFixture) publish(ctx context.Context) error {
	return appbaseimages.PublishRootBase(ctx, f.registry, io.Discard, f.in)
}

func TestRootBasePublicationCanonicalUnsignedOutput(t *testing.T) {
	fixture := newRootBaseFixture(t)
	require.NoError(t, fixture.publish(t.Context()))

	ledger, err := os.ReadFile("payload/dist/base-images.json")
	require.NoError(t, err)
	entries, err := appbaseimages.AdmitBaseImageLedger(ledger, fixture.in.ExpectedRepository)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	entry := entries[0]
	require.Equal(t, "premade-sboms/base-sbom-core.cyclonedx.json", entry.SBOM)
	canonical, err := os.ReadFile(filepath.Join("payload", entry.SBOM))
	require.NoError(t, err)
	first, err := os.ReadFile("payload/premade-sboms/inventory-root-amd64.cyclonedx.json")
	require.NoError(t, err)
	require.Equal(t, first, canonical)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(canonical)), entry.SBOMSHA256)
	require.Equal(t, "opaque-consumer-identity", entry.Provenance["material"])
	require.Len(t, fixture.syft.targets, 2)

	for _, arch := range []string{"amd64", "arm64"} {
		require.FileExists(t, "payload/premade-sboms/inventory-root-"+arch+".cyclonedx.json")
		require.FileExists(t, "payload/premade-sboms/trivy-results-root-"+arch+".json")
	}

	require.FileExists(t, "payload/premade-sboms/index-root.json")
	require.Len(t, fixture.registry.writes, 3)

	for _, ref := range fixture.registry.writes {
		require.Contains(t, ref, ":staging-")
	}

	require.NotContains(t, fixture.registry.raw, entry.FinalTag)
	require.NoFileExists(t, "payload/dist/release-images.json")
	require.NoFileExists(t, "payload/dist/base-images-publication.json")

	_, err = apprelease.CanonicalPayloadDigest("payload")
	require.NoError(t, err)
	require.ErrorIs(t, appcontainer.ValidateNativeReleaseImagesPlan(fixture.in.Plan), errs.ErrInvalidConfig)
}

func TestRootBasePublicationSelectedPlatformsAndContentRevision(t *testing.T) {
	for _, selection := range []string{"arm64", "arm64\namd64"} {
		t.Run(selection, func(t *testing.T) {
			fixture := newRootBaseFixture(t)
			image := &fixture.in.Plan.Images[0]
			image.Archs, image.Revision = selection, fixture.in.SourceSHA
			fixture.in.SourceSHA = strings.Repeat("c", 40)

			for idx := range fixture.in.Plan.Builds {
				fixture.in.Plan.Builds[idx].Revision = image.Revision
			}

			if selection == "arm64" {
				fixture.in.Plan.Builds = fixture.in.Plan.Builds[1:]

				require.NoError(t, os.Rename("layouts/root-arch-amd64", "unselected"))
			}

			require.NoError(t, fixture.publish(t.Context()))

			canonical, err := os.ReadFile("payload/premade-sboms/base-sbom-core.cyclonedx.json")
			require.NoError(t, err)
			first, err := os.ReadFile("payload/premade-sboms/inventory-root-arm64.cyclonedx.json")
			require.NoError(t, err)
			require.Equal(t, first, canonical, "the selected order, not host architecture, chooses the legacy base SBOM")
			require.Len(t, fixture.syft.targets, len(strings.Fields(selection)))
		})
	}
}

func TestRootBasePublicationRefusesIncompleteOrUnsupportedInputs(t *testing.T) {
	cases := map[string]func(*testing.T, *rootBaseFixture){
		"missing build":        func(_ *testing.T, f *rootBaseFixture) { f.in.Plan.Builds = f.in.Plan.Builds[:1] },
		"wrong build platform": func(_ *testing.T, f *rootBaseFixture) { f.in.Plan.Builds[1].Platform = "linux/amd64" },
		"scan opt out":         func(_ *testing.T, f *rootBaseFixture) { f.in.Plan.Images[0].Scan = false },
		"foreign repository":   func(_ *testing.T, f *rootBaseFixture) { f.in.ExpectedRepository += "-other" },
		"parent lineage":       func(_ *testing.T, f *rootBaseFixture) { f.in.Plan.Images[0].ProvenanceJSON = `{"parent_base":null}` },
		"parent ref": func(_ *testing.T, f *rootBaseFixture) {
			f.in.Plan.Images[0].BaseRef = "registry.example/org/parent@sha256:" + strings.Repeat("c", 64)
		},
		"multiple images": func(_ *testing.T, f *rootBaseFixture) {
			f.in.Plan.Images = append(f.in.Plan.Images, f.in.Plan.Images[0])
		},
		"release image":   func(_ *testing.T, f *rootBaseFixture) { f.in.Plan.Images[0].ImageKind = "release" },
		"missing gate":    func(_ *testing.T, f *rootBaseFixture) { f.in.Gate = nil },
		"missing scanner": func(_ *testing.T, f *rootBaseFixture) { f.in.Trivy = nil },
		"missing syft":    func(_ *testing.T, f *rootBaseFixture) { f.in.Syft = nil },
		"moving tag":      func(_ *testing.T, f *rootBaseFixture) { f.in.Plan.Images[0].MovingTagName = "core" },
		"prepared parent": func(_ *testing.T, f *rootBaseFixture) {
			parent := "registry.example/org/parent@sha256:" + strings.Repeat("c", 64)
			f.in.Plan.Builds[0].BuildArgs = "PARENT=" + parent
			f.in.Plan.Builds[0].RegistryInputs = map[string]string{"PARENT": parent}
		},
		"architecture cache": func(_ *testing.T, f *rootBaseFixture) {
			image := &f.in.Plan.Images[0]
			image.CacheTagPrefix, image.Revision = "cache-root", strings.Repeat("c", 64)

			for idx := range f.in.Plan.Builds {
				build := &f.in.Plan.Builds[idx]
				build.Revision, build.RequiredLabels = image.Revision, image.RequiredLabels
				build.CacheTag, build.BuilderMaterialID = "cache-root-"+build.Arch, strings.Repeat("d", 64)
			}
		},
		"missing layout": func(t *testing.T, _ *rootBaseFixture) {
			t.Helper()
			require.NoError(t, os.Rename("layouts/root-arch-arm64", "saved"))
		},
		"extra entry": func(t *testing.T, _ *rootBaseFixture) {
			t.Helper()
			require.NoError(t, os.WriteFile("layouts/unplanned", nil, 0o600))
		},
		"linked metadata": func(t *testing.T, _ *rootBaseFixture) {
			t.Helper()
			require.NoError(t, os.Rename("layouts/root-arch-arm64/root-arm64.json", "saved.json"))
			require.NoError(t, os.Symlink("../../saved.json", "layouts/root-arch-arm64/root-arm64.json"))
		},
		"output inside layouts": func(_ *testing.T, f *rootBaseFixture) { f.in.OutputDir = "layouts/output" },
		"linked output parent inside layouts": func(t *testing.T, f *rootBaseFixture) {
			t.Helper()
			require.NoError(t, os.Symlink("layouts", "alias"))

			f.in.OutputDir = "alias/output"
		},
		"preseeded payload": func(t *testing.T, _ *rootBaseFixture) { t.Helper(); require.NoError(t, os.Mkdir("payload", 0o700)) },
		"wrong revision":    func(_ *testing.T, f *rootBaseFixture) { f.in.SourceSHA = strings.Repeat("c", 40) },
		"missing required label": func(_ *testing.T, f *rootBaseFixture) {
			f.in.Plan.Images[0].RequiredLabels = "org.example.required=yes"
		},
		"broken layout": func(t *testing.T, _ *rootBaseFixture) {
			t.Helper()
			require.NoError(t, os.WriteFile("layouts/root-arch-arm64/layout/oci-layout", []byte("broken"), 0o600))
		},
		"bad inventory":   func(_ *testing.T, f *rootBaseFixture) { f.syft.bad = true },
		"bad scan report": func(_ *testing.T, f *rootBaseFixture) { f.in.Trivy = &fakeImageEvidenceTrivy{jsonBody: `not JSON`} },
		"layout changed after gates": func(_ *testing.T, f *rootBaseFixture) {
			f.in.Gate = func(_ context.Context, _, platform string) error {
				if platform == "linux/arm64" {
					return os.WriteFile("layouts/root-arch-amd64/layout/oci-layout", []byte("changed"), 0o600)
				}

				return nil
			}
		},
		"last gate fails": func(_ *testing.T, f *rootBaseFixture) {
			f.in.Gate = func(_ context.Context, _, platform string) error {
				f.gated = append(f.gated, platform)
				if platform == "linux/arm64" {
					return errs.ErrValidation
				}

				return nil
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRootBaseFixture(t)
			mutate(t, f)
			require.Error(t, f.publish(t.Context()))
			require.Empty(t, f.registry.writes)
			require.NoFileExists(t, filepath.Join(f.in.OutputDir, "dist/base-images.json"))
		})
	}
}

// The verifier recognizes only this test's recorded port calls. It is not a
// cryptographic verifier and does not supply publication authentication.
type rootBaseEvidence struct{ recordingImageSigner }

func rootBaseDigestRef(ref string) string {
	_, digest, _ := strings.Cut(ref, "@")

	return domaincontainer.StripTagOrDigest(ref) + "@" + digest
}

func (e *rootBaseEvidence) VerifyImage(_ context.Context, in domaincontainer.ImageVerifyRequest, _ io.Writer) error {
	if len(e.signs) != 1 || rootBaseDigestRef(e.signs[0].ImageRef) != in.ImageRef {
		return errs.ErrValidation
	}

	return nil
}

func (e *rootBaseEvidence) VerifyAttestation(_ context.Context, in domaincontainer.AttestationVerifyRequest, _ io.Writer) error {
	for _, attestation := range e.attests {
		if rootBaseDigestRef(attestation.input.ImageRef) == in.ImageRef && attestation.input.PredicateType == in.PredicateType {
			return nil
		}
	}

	return errs.ErrValidation
}

func (e *rootBaseEvidence) VerifyAttestationOutput(ctx context.Context, in domaincontainer.AttestationVerifyRequest, out, _ io.Writer) error {
	if err := e.VerifyAttestation(ctx, in, io.Discard); err != nil {
		return err
	}

	for _, attestation := range e.attests {
		if rootBaseDigestRef(attestation.input.ImageRef) == in.ImageRef && attestation.input.PredicateType == in.PredicateType {
			statement, err := json.Marshal(map[string]any{"predicateType": "https://slsa.dev/provenance/v1", "predicate": json.RawMessage(attestation.predicate)})
			if err != nil {
				return err
			}

			return json.NewEncoder(out).Encode(map[string]string{"payload": base64.StdEncoding.EncodeToString(statement)})
		}
	}

	return errs.ErrValidation
}

func importRootBasePayload(t *testing.T, fixture *rootBaseFixture) []imageledger.Entry {
	t.Helper()

	require.NoError(t, fixture.publish(t.Context()))

	digest, err := apprelease.CanonicalPayloadDigest("payload")
	require.NoError(t, err)

	transport := &pinnedHandoffTransport{}
	_, err = appartifact.Upload(t.Context(), transport, nil, io.Discard, provider.RunArtifactUpload{Name: "root-base", Dir: "payload"})
	require.NoError(t, err)

	_, err = appartifact.Download(t.Context(), transport, nil, io.Discard, provider.RunArtifactDownload{Name: "root-base", Dir: "download"})
	require.NoError(t, err)
	require.NoError(t, apprelease.StageVerifiedCanonicalPayload(t.Context(), "download", "verified", digest))
	// The producer/download tree cannot replace independently staged evidence.
	require.NoError(t, os.WriteFile("download/premade-sboms/base-sbom-core.cyclonedx.json", []byte("replacement"), 0o600))

	body, err := os.ReadFile("verified/dist/base-images.json")
	require.NoError(t, err)

	entries, err := appbaseimages.AdmitBaseImageLedger(body, fixture.in.ExpectedRepository)
	require.NoError(t, err)

	destination := "publication.example/org/base"
	imported, err := imageledger.ImportBaseImages(t.Context(), fixture.registry, entries, fixture.in.ExpectedRepository, destination)
	require.NoError(t, err)
	require.Equal(t, entries[0].SBOMSHA256, imported[0].SBOMSHA256)
	require.Equal(t, entries[0].Provenance, imported[0].Provenance)
	require.NotContains(t, fixture.registry.raw, imported[0].FinalTag)

	return imported
}

func rootBaseSigningInput(t *testing.T, f *rootBaseFixture, imported []imageledger.Entry) appcontainer.SignLedgerImagesInput {
	t.Helper()

	statement, err := appbaseimages.BaseImageProvenance(appbaseimages.BaseImageProvenanceInput{
		Entries: imported, RepositoryURL: "https://source.example/org/app", Ref: "refs/heads/main", SourceSHA: f.in.SourceSHA,
		Workflow: "bases.yml", BuilderID: "inert-builder", InvocationID: "inert-invocation", StartedOn: "2026-01-01T00:00:00Z",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("statement.json", statement, 0o600))

	sign := signInput(t, imported[0])
	sign.EvidenceRoot, sign.Entries, sign.ExpectedImageRepository = "verified", imported, domaincontainer.StripTagOrDigest(imported[0].Ref)
	sign.PredicatePath, sign.PredicateEnvelopePath = "", "statement.json"
	sign.SBOMPathPattern = `^premade-sboms/base-sbom-[-A-Za-z0-9_.]+[.]cyclonedx[.]json$`

	return sign
}

func TestRootBasePublicationTrustedBoundaryJourney(t *testing.T) {
	fixture := newRootBaseFixture(t)
	imported := importRootBasePayload(t, fixture)
	sign := rootBaseSigningInput(t, fixture, imported)
	evidence, syft := &rootBaseEvidence{}, &fakeLedgerSyft{}
	require.NoError(t, appcontainer.SignLedgerImages(t.Context(), evidence, syft, fixture.registry, io.Discard, io.Discard, sign))
	require.Empty(t, syft.targets, "trusted signer must not rescan")

	producerSBOM, err := os.ReadFile("payload/" + imported[0].SBOM)
	require.NoError(t, err)
	require.Equal(t, producerSBOM, evidence.attests[0].predicate)

	publication, err := imageledger.Marshal(imported)
	require.NoError(t, err)

	destination := sign.ExpectedImageRepository
	images, err := appbaseimages.PromotionImagesFromLedger(publication, destination)
	require.NoError(t, err)

	_, err = appbaseimages.PromoteBaseImages(t.Context(), fixture.registry, evidence, io.Discard, appbaseimages.BaseImagePromoteInput{
		Images: images, ExpectedRepository: destination, ExpectedSource: "https://source.example/org/app", ExpectedWorkflow: "bases.yml", CosignPublicKey: "inert-public-key",
	})
	require.NoError(t, err)

	final, err := fixture.registry.ResolveDigest(t.Context(), imported[0].FinalTag)
	require.NoError(t, err)
	require.Equal(t, imported[0].Digest, final)
}

func TestRootBasePublicationRejectsPayloadMutations(t *testing.T) {
	for _, mutation := range []string{"changed SBOM", "missing inventory", "reserved ledger", "linked evidence", "extra root"} {
		t.Run(mutation, func(t *testing.T) {
			fixture := newRootBaseFixture(t)
			require.NoError(t, fixture.publish(t.Context()))

			digest, err := apprelease.CanonicalPayloadDigest("payload")
			require.NoError(t, err)

			switch mutation {
			case "changed SBOM":
				require.NoError(t, os.WriteFile("payload/premade-sboms/base-sbom-core.cyclonedx.json", []byte(`{}`), 0o600))
			case "missing inventory":
				require.NoError(t, os.Remove("payload/premade-sboms/inventory-root-arm64.cyclonedx.json"))
			case "reserved ledger":
				require.NoError(t, os.WriteFile("payload/dist/base-images-publication.json", []byte(`[]`), 0o600))
			case "linked evidence":
				require.NoError(t, os.Symlink("inventory-root-amd64.cyclonedx.json", "payload/premade-sboms/link.json"))
			case "extra root":
				require.NoError(t, os.Mkdir("payload/extra", 0o700))
			}

			require.Error(t, apprelease.StageVerifiedCanonicalPayload(t.Context(), "payload", "verified", digest))
			require.NoDirExists(t, "verified")
			require.Len(t, fixture.registry.writes, 3, "no trusted registry mutation")
		})
	}
}

func TestRootBasePublicationRejectsSignerMutations(t *testing.T) {
	for _, mutation := range []string{"wrong pin", "missing pin", "traversal", "missing file", "linked file"} {
		t.Run(mutation, func(t *testing.T) {
			fixture := newRootBaseFixture(t)
			imported := importRootBasePayload(t, fixture)
			sign := rootBaseSigningInput(t, fixture, imported)

			switch mutation {
			case "wrong pin":
				sign.Entries[0].SBOMSHA256 = strings.Repeat("0", 64)
			case "missing pin":
				sign.Entries[0].SBOMSHA256 = ""
			case "traversal":
				sign.Entries[0].SBOM = "premade-sboms/../premade-sboms/base-sbom-core.cyclonedx.json"
			case "missing file":
				require.NoError(t, os.Remove("verified/"+sign.Entries[0].SBOM))
			case "linked file":
				file := "verified/" + sign.Entries[0].SBOM
				require.NoError(t, os.Rename(file, file+".saved"))
				require.NoError(t, os.Symlink(filepath.Base(file)+".saved", file))
			}

			evidence, syft := &rootBaseEvidence{}, &fakeLedgerSyft{}
			require.Error(t, appcontainer.SignLedgerImages(t.Context(), evidence, syft, fixture.registry, io.Discard, io.Discard, sign))
			require.Empty(t, evidence.signs)
			require.Empty(t, evidence.attests)
			require.Empty(t, syft.targets)
			require.Len(t, fixture.registry.writes, 4, "only unsigned source publication and import")
		})
	}
}

func TestRootBasePublicationRefusesConflictingPromotion(t *testing.T) {
	fixture := newRootBaseFixture(t)
	imported := importRootBasePayload(t, fixture)
	sign := rootBaseSigningInput(t, fixture, imported)
	evidence := &rootBaseEvidence{}
	require.NoError(t, appcontainer.SignLedgerImages(t.Context(), evidence, &fakeLedgerSyft{}, fixture.registry, io.Discard, io.Discard, sign))

	publication, err := imageledger.Marshal(imported)
	require.NoError(t, err)

	images, err := appbaseimages.PromotionImagesFromLedger(publication, sign.ExpectedImageRepository)
	require.NoError(t, err)

	fixture.registry.raw[imported[0].FinalTag] = []byte(`{"different":"immutable destination"}`)
	_, err = appbaseimages.PromoteBaseImages(t.Context(), fixture.registry, evidence, io.Discard, appbaseimages.BaseImagePromoteInput{
		Images: images, ExpectedRepository: sign.ExpectedImageRepository, ExpectedSource: "https://source.example/org/app", ExpectedWorkflow: "bases.yml", CosignPublicKey: "inert-public-key",
	})
	require.ErrorIs(t, err, errs.ErrValidation)
	require.ErrorContains(t, err, "immutable final base tag already exists with a different digest")
	require.Len(t, fixture.registry.writes, 4, "no promotion copy after immutable conflict")
}
