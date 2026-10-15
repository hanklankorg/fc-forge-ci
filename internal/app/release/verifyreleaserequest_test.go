// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

const (
	requestObject = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	requestCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	otherCommit   = "cccccccccccccccccccccccccccccccccccccccc"
)

var (
	errUnexpectedRef                 = errors.New("unexpected ref")
	errMissingLocalTag               = errors.New("missing local tag")
	errUnexpectedFetch               = errors.New("unexpected fetch")
	errUnexpectedRemoteRequestLookup = errors.New("unexpected remote request lookup")
	errUnexpectedFinalTagLookup      = errors.New("unexpected final tag lookup")
	errUnexpectedCatFileRef          = errors.New("unexpected cat-file ref")
)

type fakeReleaseRequestGit struct {
	localObject       string
	localMissingFirst bool
	remoteObject      string
	finalExists       bool
	catType           string
	verifyOK          bool
	verifyOutput      string
	verifyErr         error
	fetches           int
	verifiedTag       string
	verifiedSigners   string
	queries           int
	tagObject         string
	headCommit        string
	tagCommit         string
	remoteObjectAfter string
	remoteLookups     int
	// readTokens is the token each read of origin carried.
	readTokens []string
}

func newFakeReleaseRequestGit() *fakeReleaseRequestGit {
	return &fakeReleaseRequestGit{
		localObject:  requestObject,
		remoteObject: requestObject,
		catType:      "tag",
		verifyOK:     true,
		tagObject:    "object " + requestCommit + "\ntype commit\ntag release-request/v1.2.3\ntagger t <t@example.test> 0 +0000\n\nrelease\n",
		headCommit:   requestCommit,
		tagCommit:    requestCommit,
	}
}

func (f *fakeReleaseRequestGit) RevParse(_ context.Context, ref string) (string, error) {
	f.queries++

	switch ref {
	case "HEAD^{commit}":
		return f.headCommit, nil
	case "refs/tags/release-request/v1.2.3^{commit}":
		return f.tagCommit, nil
	}

	if ref != "refs/tags/release-request/v1.2.3" {
		return "", errUnexpectedRef
	}

	if f.localMissingFirst && f.fetches == 0 {
		return "", errMissingLocalTag
	}

	return f.localObject, nil
}

func (f *fakeReleaseRequestGit) FetchTagFromRemote(_ context.Context, remote, tag string, cred runcontext.Credential) error {
	f.recordRead(cred)

	if remote != "origin" || tag != "release-request/v1.2.3" {
		return errUnexpectedFetch
	}

	f.fetches++

	return nil
}

func (f *fakeReleaseRequestGit) RemoteTagObject(_ context.Context, remote, tag string, cred runcontext.Credential) (string, error) {
	f.recordRead(cred)

	if remote != "origin" || tag != "release-request/v1.2.3" {
		return "", errUnexpectedRemoteRequestLookup
	}

	if f.remoteObject == "" {
		return "", errs.ErrValidation
	}

	f.remoteLookups++
	if f.remoteLookups > 1 && f.remoteObjectAfter != "" {
		return f.remoteObjectAfter, nil
	}

	return f.remoteObject, nil
}

func (f *fakeReleaseRequestGit) CatFileTag(_ context.Context, ref string) (string, error) {
	if ref != "refs/tags/release-request/v1.2.3" {
		return "", errUnexpectedCatFileRef
	}

	return f.tagObject, nil
}

func (f *fakeReleaseRequestGit) RemoteTagExists(_ context.Context, remote, tag string, cred runcontext.Credential) (bool, error) {
	f.recordRead(cred)

	if remote != "origin" || tag != "v1.2.3" {
		return false, errUnexpectedFinalTagLookup
	}

	return f.finalExists, nil
}

func (f *fakeReleaseRequestGit) CatFileType(_ context.Context, ref string) (string, error) {
	if ref != "refs/tags/release-request/v1.2.3" {
		return "", errUnexpectedCatFileRef
	}

	return f.catType, nil
}

func (f *fakeReleaseRequestGit) VerifyTagSSHAgainstAllowedSigners(_ context.Context, tag, allowedSignersPath string) (bool, string, error) {
	f.verifiedTag = tag
	f.verifiedSigners = allowedSignersPath

	return f.verifyOK, f.verifyOutput, f.verifyErr
}

func (f *fakeReleaseRequestGit) recordRead(cred runcontext.Credential) {
	f.readTokens = append(f.readTokens, cred.For("https://forge.example.test/owner/repo"))
}

func writeAllowedSigners(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "allowed_signers")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func runVerifyReleaseRequest(t *testing.T, git *fakeReleaseRequestGit, signers string, request, tag string) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := apprelease.VerifyReleaseRequest(context.Background(), git, &out, apprelease.VerifyReleaseRequestInput{
		ReleaseRequest:     request,
		ReleaseTag:         tag,
		AllowedSignersPath: signers,
	})

	return out.String(), err
}

func TestVerifyReleaseRequest_Accepts(t *testing.T) {
	t.Parallel()

	git := newFakeReleaseRequestGit()
	signers := writeAllowedSigners(t, "release@example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForUnitTestsOnly\n")

	out, err := runVerifyReleaseRequest(t, git, signers, "release-request/v1.2.3", "v1.2.3")
	if err != nil {
		t.Fatalf("valid release request rejected: %v", err)
	}

	if !strings.Contains(out, "Release request signature verified: release-request/v1.2.3 -> v1.2.3") {
		t.Errorf("success output = %q", out)
	}

	if git.verifiedTag != "release-request/v1.2.3" || git.verifiedSigners != signers {
		t.Errorf("SSH verify args = (%q, %q)", git.verifiedTag, git.verifiedSigners)
	}
}

func TestVerifyReleaseRequest_RejectsNonRegularSignersBeforeQueries(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"directory", "leaf link", "parent link", "empty"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			path := writeAllowedSigners(t, "fixture signer")

			switch kind {
			case "directory":
				path = t.TempDir()
			case "empty":
				path = writeAllowedSigners(t, "")
			case "leaf link":
				link := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}

				path = link
			case "parent link":
				link := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(filepath.Dir(path), link); err != nil {
					t.Fatal(err)
				}

				path = filepath.Join(link, filepath.Base(path))
			}

			git := newFakeReleaseRequestGit()

			_, err := runVerifyReleaseRequest(t, git, path, "release-request/v1.2.3", "v1.2.3")
			if !errors.Is(err, errs.ErrValidation) {
				t.Errorf("err=%v, want ErrValidation", err)
			}

			if git.queries != 0 || git.fetches != 0 || git.verifiedTag != "" {
				t.Error("dependency called for invalid signer file")
			}
		})
	}
}

func TestVerifyReleaseRequest_FetchesRequestTagWhenMissingLocally(t *testing.T) {
	t.Parallel()

	git := newFakeReleaseRequestGit()
	git.localMissingFirst = true
	signers := writeAllowedSigners(t, "release@example.test ssh-ed25519 AAAA\n")

	if _, err := runVerifyReleaseRequest(t, git, signers, "release-request/v1.2.3", "v1.2.3"); err != nil {
		t.Fatal(err)
	}

	if git.fetches != 1 {
		t.Errorf("fetches = %d, want 1", git.fetches)
	}
}

// Every read of origin carries the caller's token, so a private repository's
// request verifies; without it Git would fall back to an interactive prompt.
func TestVerifyReleaseRequest_AuthenticatesEveryReadOfOrigin(t *testing.T) {
	t.Parallel()

	git := newFakeReleaseRequestGit()
	git.localMissingFirst = true
	signers := writeAllowedSigners(t, "release@example.test ssh-ed25519 AAAA\n")

	err := apprelease.VerifyReleaseRequest(t.Context(), git, &bytes.Buffer{}, apprelease.VerifyReleaseRequestInput{
		ReleaseRequest:     "release-request/v1.2.3",
		ReleaseTag:         "v1.2.3",
		AllowedSignersPath: signers,
		Token:              runcontext.OperatorCredential("read-token"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// fetch, request object, final-tag absence, request re-read
	if len(git.readTokens) != 4 {
		t.Fatalf("reads of origin = %d, want 4", len(git.readTokens))
	}

	for index, token := range git.readTokens {
		if token != "read-token" {
			t.Errorf("read %d carried token %q", index, token)
		}
	}
}

func TestVerifyReleaseRequest_RejectsShellRegressionModes(t *testing.T) {
	t.Parallel()

	signers := writeAllowedSigners(t, "release@example.test ssh-ed25519 AAAA\n")

	// This gate decides whether a release publishes, so each refusal is
	// pinned to its class rather than to "some error": a caller mapping
	// them to exit codes has to be able to tell them apart, and a bare
	// nil-check passes on a failure from anywhere in the chain.
	cases := map[string]struct {
		mutate func(*fakeReleaseRequestGit)
		want   error
	}{
		"final_exists":  {mutate: func(g *fakeReleaseRequestGit) { g.finalExists = true }, want: errs.ErrValidation},
		"mismatch":      {mutate: func(g *fakeReleaseRequestGit) { g.localObject = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }, want: errs.ErrValidation},
		"lightweight":   {mutate: func(g *fakeReleaseRequestGit) { g.catType = "commit" }, want: errs.ErrValidation},
		"bad_signature": {mutate: func(g *fakeReleaseRequestGit) { g.verifyErr = errs.ErrValidation }, want: errs.ErrValidation},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			git := newFakeReleaseRequestGit()
			testCase.mutate(git)

			if _, err := runVerifyReleaseRequest(t, git, signers, "release-request/v1.2.3", "v1.2.3"); !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestVerifyReleaseRequest_RejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	signers := writeAllowedSigners(t, "release@example.test ssh-ed25519 AAAA\n")
	cases := map[string]struct {
		request string
		tag     string
	}{
		"unstable_request": {"release-request/v1.2.3-rc1", "v1.2.3-rc1"},
		"unstable_tag":     {"release-request/v1.2.3", "v1.2.3-rc1"},
		"mismatched_tag":   {"release-request/v1.2.3", "v1.2.4"},
		"not_request":      {"v1.2.3", "v1.2.3"},
		"qualified_ref":    {"refs/tags/release-request/v1.2.3", "v1.2.3"},
		"multi_line":       {"release-request/v1.2.3\n", "v1.2.3"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := runVerifyReleaseRequest(t, newFakeReleaseRequestGit(), signers, tc.request, tc.tag)
			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("err = %v, want validation", err)
			}
		})
	}
}

func TestVerifyReleaseRequest_RejectsMissingOrEmptyAllowedSigners(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing_allowed_signers")
	empty := writeAllowedSigners(t, "")

	for name, path := range map[string]string{"missing": missing, "empty": empty} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := runVerifyReleaseRequest(t, newFakeReleaseRequestGit(), path, "release-request/v1.2.3", "v1.2.3")
			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("err = %v, want validation", err)
			}
		})
	}
}

func TestVerifyReleaseRequest_MissingInputsAreUsage(t *testing.T) {
	t.Parallel()

	err := apprelease.VerifyReleaseRequest(context.Background(), newFakeReleaseRequestGit(), nil, apprelease.VerifyReleaseRequestInput{})
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("err = %v, want usage", err)
	}
}

func TestVerifyReleaseRequest_RejectsRemoteRequestMissing(t *testing.T) {
	t.Parallel()

	git := newFakeReleaseRequestGit()
	git.remoteObject = ""
	signers := writeAllowedSigners(t, "release@example.test ssh-ed25519 AAAA\n")

	_, err := runVerifyReleaseRequest(t, git, signers, "release-request/v1.2.3", "v1.2.3")
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("err = %v, want validation", err)
	}
}

func TestVerifyReleaseRequest_RejectsFalseVerificationWithoutError(t *testing.T) {
	t.Parallel()

	git := newFakeReleaseRequestGit()
	git.verifyOK = false
	git.verifyOutput = "No principal matched"
	signers := writeAllowedSigners(t, "release@example.test ssh-ed25519 AAAA\n")

	_, err := runVerifyReleaseRequest(t, git, signers, "release-request/v1.2.3", "v1.2.3")
	if !errors.Is(err, errs.ErrPermissionDenied) {
		t.Fatalf("err = %v, want permission denied", err)
	}
}

// The checks a caller needs immediately before it loads a signing key: the
// request and the checkout are at the commit the caller took from its trusted
// event context, the tag object names itself, and origin still publishes the
// same object once every other check has passed.
func TestVerifyReleaseRequest_BindsTheCommitAndRefusesADriftingRequest(t *testing.T) {
	t.Parallel()

	signers := writeAllowedSigners(t, "release@example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForUnitTestsOnly\n")

	for _, test := range []struct {
		name   string
		mutate func(*fakeReleaseRequestGit)
		ok     bool
	}{
		{"authorised request", func(*fakeReleaseRequestGit) {}, true},
		{"request tag moved off the event commit", func(f *fakeReleaseRequestGit) { f.tagCommit = otherCommit }, false},
		{"checkout is not the request commit", func(f *fakeReleaseRequestGit) { f.headCommit = otherCommit }, false},
		{"tag object names another release", func(f *fakeReleaseRequestGit) {
			f.tagObject = strings.Replace(f.tagObject, "tag release-request/v1.2.3", "tag release-request/v9.9.9", 1)
		}, false},
		{"tag object carries no name", func(f *fakeReleaseRequestGit) { f.tagObject = "object x\ntype commit\n\nbody\n" }, false},
		{"request replaced during validation", func(f *fakeReleaseRequestGit) {
			f.remoteObjectAfter = "dddddddddddddddddddddddddddddddddddddddd"
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			git := newFakeReleaseRequestGit()
			test.mutate(git)

			var out bytes.Buffer

			err := apprelease.VerifyReleaseRequest(context.Background(), git, &out, apprelease.VerifyReleaseRequestInput{
				ReleaseRequest:     "release-request/v1.2.3",
				ReleaseTag:         "v1.2.3",
				AllowedSignersPath: signers,
				ExpectedCommit:     requestCommit,
			})
			if test.ok {
				if err != nil {
					t.Fatalf("authorised request rejected: %v", err)
				}

				return
			}

			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("err = %v, want ErrValidation", err)
			}
		})
	}
}

// An expected commit that is not a full OID is a caller mistake, refused
// before any repository query.
func TestVerifyReleaseRequest_RefusesAShortExpectedCommit(t *testing.T) {
	t.Parallel()

	git := newFakeReleaseRequestGit()

	err := apprelease.VerifyReleaseRequest(context.Background(), git, &bytes.Buffer{}, apprelease.VerifyReleaseRequestInput{
		ReleaseRequest:     "release-request/v1.2.3",
		ReleaseTag:         "v1.2.3",
		AllowedSignersPath: writeAllowedSigners(t, "signer"),
		ExpectedCommit:     "bbbbbbb",
	})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}

	if git.queries != 0 {
		t.Error("repository queried for an invalid expected commit")
	}
}

// --skip-signature keeps every other check, and says so in the output.
func TestVerifyReleaseRequest_SkipSignatureKeepsTheOtherChecks(t *testing.T) {
	t.Parallel()

	git := newFakeReleaseRequestGit()
	git.verifyOK = false

	var out bytes.Buffer

	input := apprelease.VerifyReleaseRequestInput{
		ReleaseRequest: "release-request/v1.2.3",
		ReleaseTag:     "v1.2.3",
		ExpectedCommit: requestCommit,
		SkipSignature:  true,
	}

	if err := apprelease.VerifyReleaseRequest(context.Background(), git, &out, input); err != nil {
		t.Fatalf("skip-signature run rejected: %v", err)
	}

	if git.verifiedTag != "" {
		t.Error("signature verified although it was skipped")
	}

	if !strings.Contains(out.String(), "WITHOUT signature verification") {
		t.Errorf("output = %q", out.String())
	}

	git = newFakeReleaseRequestGit()
	git.catType = "commit"

	if err := apprelease.VerifyReleaseRequest(context.Background(), git, &bytes.Buffer{}, input); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("lightweight tag accepted without signature verification: %v", err)
	}
}
