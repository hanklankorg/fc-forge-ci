// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package fakeprovider_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeprovider"
)

// Compile-time check: Fake satisfies provider.Provider.
var _ provider.Provider = (*fakeprovider.Fake)(nil)

func TestFake_ReleaseRecordingOwnership(t *testing.T) {
	t.Parallel()

	for _, method := range []string{"create", "publish"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			wantErr := errors.New("release rejected") //nolint:err113 // test mock error
			fake := fakeprovider.New(t).WithCreateReleaseError(wantErr).WithPublishReleaseError(wantErr)
			invoke, snapshot := fake.CreateRelease, fake.CreateReleaseCalls
			wantCount := fakeprovider.Calls{CreateRelease: 4}

			if method == "publish" {
				invoke, snapshot = fake.PublishRelease, fake.PublishReleaseCalls
				wantCount = fakeprovider.Calls{PublishRelease: 4}
			}

			require.Empty(t, snapshot())

			want := []fakeprovider.ReleaseCall{
				{Repo: "owner/first", Spec: provider.ReleaseSpec{
					Tag: "v1", Name: "First", NotesFile: "notes.md", Draft: true,
					Prerelease: true, MakeLatest: provider.MakeLatestFalse, Assets: []string{"one.zip", "two.zip"},
				}},
				{Repo: "owner/second", Spec: provider.ReleaseSpec{Tag: "v2", Assets: []string{"three.zip"}}},
				{Repo: "owner/nil", Spec: provider.ReleaseSpec{Tag: "v3"}},
				{Repo: "owner/empty", Spec: provider.ReleaseSpec{Tag: "v4", Assets: []string{}}},
			}
			for _, call := range want {
				spec := call.Spec
				spec.Assets = slices.Clone(spec.Assets)
				require.ErrorIs(t, invoke(t.Context(), call.Repo, spec), wantErr)

				for index := range spec.Assets {
					spec.Assets[index] = "caller mutation"
				}
			}

			got := snapshot()
			require.Equal(t, want, got)
			require.Equal(t, wantCount, fake.Calls())

			for index := range got {
				got[index].Repo = "snapshot mutation"

				got[index].Spec.Tag = "wrong tag"
				for asset := range got[index].Spec.Assets {
					got[index].Spec.Assets[asset] = "snapshot mutation"
				}
			}

			require.Equal(t, want, snapshot())
		})
	}
}

func TestFake_SARIFRecordingOwnership(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("upload rejected") //nolint:err113 // test mock error
	fake := fakeprovider.New(t).WithUploadSARIFError(wantErr)
	require.Empty(t, fake.UploadSARIFCalls())

	want := []provider.SARIFUpload{
		{Repository: "owner/first", SHA: "abc123", Ref: "refs/heads/main", Token: "synthetic-first", SARIF: []byte(`{"runs":[]}`)},
		{Repository: "owner/second", SHA: "def456", Ref: "refs/heads/dev", Token: "synthetic-second", SARIF: []byte("second\n")},
		{Repository: "owner/nil"},
		{Repository: "owner/empty", SARIF: []byte{}},
	}
	for _, upload := range want {
		upload.SARIF = slices.Clone(upload.SARIF)
		require.ErrorIs(t, fake.UploadSARIF(t.Context(), upload), wantErr)

		for index := range upload.SARIF {
			upload.SARIF[index] = 'X'
		}
	}

	got := fake.UploadSARIFCalls()
	require.Equal(t, want, got)

	for index := range got {
		got[index].Repository = "snapshot mutation"
		for pos := range got[index].SARIF {
			got[index].SARIF[pos] = 'Y'
		}
	}

	require.Equal(t, want, fake.UploadSARIFCalls())
}

// Two properties of this fake are worth pinning, and they are the two that
// constrain its implementation rather than its configuration.
//
// Most of what a provider fake could be asked to prove is vacuous by
// construction: asserting that WithRepoMetadata makes FetchRepoMetadata return
// that metadata, or that the description default is the description default,
// restates the fixture the test just set. What cannot be read off the
// configuration is whether the recorder hands out its own storage, and whether
// one call counts as one call.

// TestFake_RecordedArgumentsAreDetachedSnapshots proves an assertion cannot
// corrupt what a later assertion reads.
//
// Every recorder accessor builds a copy, but nothing checked it. Returning the
// backing slice would make the first test that sorts or truncates a recorded
// call list silently change what every later read of the same fake sees — and
// the symptom would appear in an unrelated assertion, which is the hardest kind
// of test failure to trace.
func TestFake_RecordedArgumentsAreDetachedSnapshots(t *testing.T) {
	t.Parallel()

	fake := fakeprovider.New(t).WithPlatform(provider.ForgeGitHub)

	for _, repo := range []string{"owner/one", "owner/two"} {
		//nolint:gosec // G101: fake token literal, not a credential.
		if err := fake.ValidateToken(t.Context(), "github_pat_fixture", repo); err != nil {
			t.Fatal(err)
		}

		if _, err := fake.ValidateBotPermissions(t.Context(), repo); err != nil {
			t.Fatal(err)
		}
	}

	tokenCalls := fake.ValidateTokenCalls()
	botArgs := fake.ValidateBotPermissionsArgs()

	if len(tokenCalls) != 2 || len(botArgs) != 2 {
		t.Fatalf("recorded %d token and %d bot calls, want 2 each", len(tokenCalls), len(botArgs))
	}

	// Corrupt what we were handed.
	tokenCalls[0].Repo = "mutated"
	tokenCalls[1] = fakeprovider.TokenCall{}
	botArgs[0] = "mutated"

	if got := fake.ValidateTokenCalls(); got[0].Repo != "owner/one" || got[1].Repo != "owner/two" {
		t.Errorf("token calls after caller mutation = %+v, want the recorded pair", got)
	}

	if got := fake.ValidateBotPermissionsArgs(); !slices.Equal(got, []string{"owner/one", "owner/two"}) {
		t.Errorf("bot args after caller mutation = %v, want the recorded pair", got)
	}
}

// TestFake_CountersCountEachCallExactlyOnce pins the counters against the two
// ways they go wrong: not incrementing, and incrementing more than once.
//
// Tests assert "this was called once" or "this was never called" through these,
// so a counter that double-counts turns a correct product into a failing test,
// and one that never counts makes "never called" true no matter what happened —
// which is the direction that hides a bug rather than inventing one.
func TestFake_CountersCountEachCallExactlyOnce(t *testing.T) {
	t.Parallel()

	fake := fakeprovider.New(t).WithPlatform(provider.ForgeGitHub)

	if got := fake.Calls(); got != (fakeprovider.Calls{}) {
		t.Fatalf("a fresh fake reports %+v, want all zero", got)
	}

	_ = fake.Name()

	if _, err := fake.FetchRepoMetadata(t.Context(), "owner/repo"); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // G101: fake token literal, not a credential.
	if err := fake.ValidateToken(t.Context(), "github_pat_fixture", "owner/repo"); err != nil {
		t.Fatal(err)
	}

	got := fake.Calls()
	if got.Name != 1 || got.FetchRepoMetadata != 1 || got.ValidateToken != 1 {
		t.Errorf("counters = %+v, want exactly one of each called method", got)
	}

	// Methods that were not called stay at zero, so "never called" means it.
	if got.CreateRelease != 0 || got.PublishRelease != 0 || got.UploadReleaseAsset != 0 || got.ValidateBotPermissions != 0 {
		t.Errorf("counters = %+v, want zero for the methods never invoked", got)
	}
}
