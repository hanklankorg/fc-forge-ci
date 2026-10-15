// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/app/baseimages"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

const signRepository = "registry.example/owner/app-base"

func signLedgerEntry() imageledger.Entry {
	digest := "sha256:" + strings.Repeat("a", 64)
	inputID := strings.Repeat("b", 64)

	return imageledger.Entry{
		Role: "base", ImageKind: imageledger.ImageKindBase, Flavor: "alpine",
		Ref: signRepository + "@" + digest, Digest: digest,
		FinalTag: signRepository + ":" + inputID + "-alpine", BaseInputID: inputID,
	}
}

func TestAdmitBaseImageLedger(t *testing.T) {
	t.Parallel()

	marshal := func(mutate func(*imageledger.Entry)) []byte {
		entry := signLedgerEntry()
		mutate(&entry)

		body, err := imageledger.Marshal([]imageledger.Entry{entry})
		require.NoError(t, err)

		return body
	}

	for _, tc := range []struct {
		name       string
		ledger     []byte
		repository string
		want       error
	}{
		{name: "base entry inside the repository", ledger: marshal(func(*imageledger.Entry) {}), repository: signRepository},
		{name: "not JSON", ledger: []byte("{"), repository: signRepository, want: errs.ErrMalformedInput},
		{name: "unknown field", ledger: []byte(`[{"surprise":true}]`), repository: signRepository, want: errs.ErrMalformedInput},
		{name: "no entries", ledger: []byte("[]"), repository: signRepository, want: errs.ErrValidation},
		{name: "release entry", ledger: marshal(func(e *imageledger.Entry) { e.ImageKind = imageledger.ImageKindRelease }), repository: signRepository, want: errs.ErrValidation},
		{name: "entry without a kind", ledger: marshal(func(e *imageledger.Entry) { e.ImageKind = "" }), repository: signRepository, want: errs.ErrValidation},
		{name: "malformed digest", ledger: marshal(func(e *imageledger.Entry) { e.Digest = "sha256:short" }), repository: signRepository, want: errs.ErrValidation},
		{name: "ref outside the repository", ledger: marshal(func(e *imageledger.Entry) { e.Ref = "registry.example/owner/other@" + e.Digest }), repository: signRepository, want: errs.ErrValidation},
		{name: "final tag outside the repository", ledger: marshal(func(e *imageledger.Entry) { e.FinalTag = "registry.example/owner/other:tag" }), repository: signRepository, want: errs.ErrValidation},
		{name: "no expected repository", ledger: marshal(func(*imageledger.Entry) {}), want: errs.ErrMissingInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			entries, err := baseimages.AdmitBaseImageLedger(tc.ledger, tc.repository)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				require.Empty(t, entries)

				return
			}

			require.NoError(t, err)
			require.Equal(t, []imageledger.Entry{signLedgerEntry()}, entries)
		})
	}
}

func TestBaseImageProvenance(t *testing.T) {
	t.Parallel()

	valid := func() baseimages.BaseImageProvenanceInput {
		return baseimages.BaseImageProvenanceInput{
			Entries:       []imageledger.Entry{signLedgerEntry()},
			RepositoryURL: "https://forge.example/Owner/App",
			Ref:           "main",
			SourceSHA:     strings.Repeat("c", 40),
			Workflow:      "build-base-images.yml",
			BuilderID:     "https://forge.example/Owner/App/.forgejo/workflows/sign.yml@refs/heads/main",
			InvocationID:  "https://forge.example/Owner/App/actions/runs/7",
			StartedOn:     "2026-01-02T03:04:05Z",
		}
	}

	t.Run("names the ledger digests, the source and the caller", func(t *testing.T) {
		t.Parallel()

		body, err := baseimages.BaseImageProvenance(valid())
		require.NoError(t, err)

		var statement struct {
			Subject []struct {
				Name   string            `json:"name"`
				Digest map[string]string `json:"digest"`
			} `json:"subject"`
			Predicate struct {
				BuildDefinition struct {
					ExternalParameters   map[string]any `json:"externalParameters"`
					ResolvedDependencies []struct {
						URI    string            `json:"uri"`
						Digest map[string]string `json:"digest"`
					} `json:"resolvedDependencies"`
				} `json:"buildDefinition"`
			} `json:"predicate"`
		}
		require.NoError(t, json.Unmarshal(body, &statement))

		require.Len(t, statement.Subject, 1)
		require.Equal(t, signRepository, statement.Subject[0].Name)
		require.Equal(t, map[string]string{"sha256": strings.Repeat("a", 64)}, statement.Subject[0].Digest)
		require.Equal(t, map[string]any{
			"source":   "git+https://forge.example/Owner/App",
			"ref":      "main",
			"workflow": "build-base-images.yml",
		}, statement.Predicate.BuildDefinition.ExternalParameters)
		require.Len(t, statement.Predicate.BuildDefinition.ResolvedDependencies, 1)
		require.Equal(t, "git+https://forge.example/Owner/App@main", statement.Predicate.BuildDefinition.ResolvedDependencies[0].URI)
		require.Equal(t, map[string]string{"gitCommit": strings.Repeat("c", 40)}, statement.Predicate.BuildDefinition.ResolvedDependencies[0].Digest)
	})

	for _, tc := range []struct {
		name   string
		mutate func(*baseimages.BaseImageProvenanceInput)
		want   error
	}{
		{name: "no workflow", mutate: func(in *baseimages.BaseImageProvenanceInput) { in.Workflow = "" }, want: errs.ErrUsage},
		{name: "absolute workflow", mutate: func(in *baseimages.BaseImageProvenanceInput) { in.Workflow = "/etc/passwd" }, want: errs.ErrUsage},
		{name: "climbing workflow", mutate: func(in *baseimages.BaseImageProvenanceInput) { in.Workflow = "../build.yml" }, want: errs.ErrUsage},
		{name: "control character in workflow", mutate: func(in *baseimages.BaseImageProvenanceInput) { in.Workflow = "build\x1b.yml" }, want: errs.ErrUsage},
		{name: "no commit", mutate: func(in *baseimages.BaseImageProvenanceInput) { in.SourceSHA = "" }, want: errs.ErrValidation},
		{name: "abbreviated commit", mutate: func(in *baseimages.BaseImageProvenanceInput) { in.SourceSHA = "abc1234" }, want: errs.ErrValidation},
		{name: "no builder identity", mutate: func(in *baseimages.BaseImageProvenanceInput) { in.BuilderID = "" }, want: errs.ErrUsage},
		{name: "no entries", mutate: func(in *baseimages.BaseImageProvenanceInput) { in.Entries = nil }, want: errs.ErrUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := valid()
			tc.mutate(&in)

			body, err := baseimages.BaseImageProvenance(in)
			require.ErrorIs(t, err, tc.want)
			require.Empty(t, body)
		})
	}
}
