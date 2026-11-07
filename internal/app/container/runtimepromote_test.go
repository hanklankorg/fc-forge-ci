// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/stretchr/testify/require"
)

type runtimePromotionRegistry struct {
	*fakeSignerImageTool

	tags      map[string]string
	copies    []string
	copyErr   error
	corrupt   bool
	servedRaw []byte
}

func (r *runtimePromotionRegistry) Manifest(ctx context.Context, ref string) ([]byte, error) {
	if strings.Contains(ref, ":sha-") && r.servedRaw != nil {
		return r.servedRaw, nil
	}

	return r.fakeSignerImageTool.Manifest(ctx, ref)
}

func (r *runtimePromotionRegistry) ResolveDigest(_ context.Context, ref string) (string, error) {
	if digest, ok := r.tags[ref]; ok {
		return digest, nil
	}

	return "", errs.ErrMissingInput
}

func (r *runtimePromotionRegistry) CopyTag(_ context.Context, source, destination string) error {
	r.copies = append(r.copies, source+" -> "+destination)
	if r.copyErr != nil {
		return r.copyErr
	}

	_, digest, _ := strings.Cut(source, "@")
	if r.corrupt {
		digest = "sha256:" + strings.Repeat("f", 64)
	}

	r.tags[destination] = digest

	return nil
}

type runtimeBranchReader func(context.Context, string, string, string) (string, error)

func (f runtimeBranchReader) BranchHead(ctx context.Context, server, repository, branch string) (string, error) {
	return f(ctx, server, repository, branch)
}

func TestRuntimePromotionVerifiesAllEvidenceBeforeFreshnessAndCopy(t *testing.T) {
	in, image := runtimeSigningFixture(t)
	registry := &runtimePromotionRegistry{fakeSignerImageTool: image, tags: map[string]string{}}
	verifier := &fakeReleaseImageVerifier{}
	source := runtimeBranchReader(func(_ context.Context, server, repository, branch string) (string, error) {
		require.Equal(t, in.Manifest.ServerURL, server)
		require.Equal(t, in.Manifest.Repository, repository)
		require.Equal(t, "main", branch)
		require.Len(t, verifier.requests, 6, "all native signatures and attestations must precede the freshness check")
		require.Empty(t, registry.copies)

		return in.Manifest.SourceSHA, nil
	})
	ref, err := appcontainer.PromoteRuntimePublication(t.Context(), registry, verifier, source, io.Discard,
		appcontainer.RuntimePublicationPromoteInput{Manifest: in.Manifest, PublicKey: "cosign.pub"})
	require.NoError(t, err)

	final := in.Manifest.ImageRepository + ":sha-" + in.Manifest.SourceSHA
	require.Equal(t, []string{ref + " -> " + final}, registry.copies)
	require.Equal(t, manifestTestDigest(image.raw), registry.tags[final])
}

func TestRuntimePromotionRefusesUnsafePublicationAndNeverRetriesCopies(t *testing.T) {
	want := errors.New("publication transport failed")

	for _, failure := range []string{"stale main", "source unavailable", "signature", "attestation", "changed index", "copy failure", "changed destination", "changed served bytes"} {
		t.Run(failure, func(t *testing.T) {
			in, image := runtimeSigningFixture(t)
			registry := &runtimePromotionRegistry{fakeSignerImageTool: image, tags: map[string]string{}}
			verifier := &fakeReleaseImageVerifier{}

			switch failure {
			case "signature":
				verifier.verifyErr = want
			case "attestation":
				verifier.attestErr = want
			case "changed index":
				image.raw = []byte(`{"another":"index"}`)
			case "copy failure":
				registry.copyErr = want
			case "changed destination":
				registry.corrupt = true
			case "changed served bytes":
				registry.servedRaw = []byte(`{"changed":"bytes"}`)
			}

			source := runtimeBranchReader(func(context.Context, string, string, string) (string, error) {
				if failure == "source unavailable" {
					return "", want
				}

				if failure == "stale main" {
					return strings.Repeat("b", 40), nil
				}

				return in.Manifest.SourceSHA, nil
			})
			ref, err := appcontainer.PromoteRuntimePublication(t.Context(), registry, verifier, source, io.Discard,
				appcontainer.RuntimePublicationPromoteInput{Manifest: in.Manifest, PublicKey: "cosign.pub"})
			require.Error(t, err)
			require.Empty(t, ref, "failure cannot report a published pin")

			if failure == "copy failure" || failure == "changed destination" || failure == "changed served bytes" {
				require.Len(t, registry.copies, 1, "an uncertain copy must not be retried")
			} else {
				require.Empty(t, registry.copies)
			}

			if failure == "copy failure" || failure == "source unavailable" || failure == "signature" || failure == "attestation" {
				require.ErrorIs(t, err, want)
			}
		})
	}
}

func TestRuntimePromotionPreservesTheSourceTagPointerBehavior(t *testing.T) {
	in, image := runtimeSigningFixture(t)
	final := in.Manifest.ImageRepository + ":sha-" + in.Manifest.SourceSHA
	registry := &runtimePromotionRegistry{fakeSignerImageTool: image, tags: map[string]string{final: "sha256:" + strings.Repeat("f", 64)}}
	source := runtimeBranchReader(func(context.Context, string, string, string) (string, error) { return in.Manifest.SourceSHA, nil })
	ref, err := appcontainer.PromoteRuntimePublication(t.Context(), registry, &fakeReleaseImageVerifier{}, source, io.Discard,
		appcontainer.RuntimePublicationPromoteInput{Manifest: in.Manifest, PublicKey: "cosign.pub"})
	require.NoError(t, err)
	require.NotEmpty(t, ref)
	require.Len(t, registry.copies, 1)
	require.Equal(t, manifestTestDigest(image.raw), registry.tags[final])
}
