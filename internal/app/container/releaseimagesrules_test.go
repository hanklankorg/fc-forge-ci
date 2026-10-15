// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

// TestValidateReleaseImagesPath_ConfinesTheLedgerUnderDist keeps the image
// ledger inside the directory the release is assembled from. Every refusal is
// checked against errs.ErrUsage rather than merely being non-nil: these inputs
// are caller mistakes, and the exit-code ladder depends on that distinction --
// a validator that started returning a different class here would still look
// correct to a bare nil-check.
func TestValidateReleaseImagesPath_ConfinesTheLedgerUnderDist(t *testing.T) {
	t.Parallel()

	if err := appcontainer.ValidateReleaseImagesPath("dist/release-images.json", "dist"); err != nil {
		t.Fatalf("valid confined path rejected: %v", err)
	}

	for name, path := range map[string]string{
		"outside dist":            "release-images.json",
		"dist itself":             "dist",
		"parent step out of dist": "dist/../release-images.json",
		"nested parent steps":     "dist/sub/../../release-images.json",
		// Refused since the '..' detection moved to pathsafe.Relative --
		// deliberate tightenings, pinned so they stay refused: control
		// characters have no legitimate producer in a CI flag value, and a
		// doubled slash makes the under-dist suffix read as absolute.
		"a tab in the name":          "dist/release\timages.json",
		"a trailing newline":         "dist/release-images.json\n",
		"a doubled slash after dist": "dist//release-images.json",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := appcontainer.ValidateReleaseImagesPath(path, "dist"); !errors.Is(err, errs.ErrUsage) {
				t.Errorf("path %q: err = %v, want errs.ErrUsage", path, err)
			}
		})
	}
}

func TestDefaultReleaseImageRepository_LowercasesTheRepo(t *testing.T) {
	t.Parallel()

	got, err := appcontainer.DefaultReleaseImageRepository("codeberg.org", "Itiquette/Nanolinter")
	if err != nil {
		t.Fatal(err)
	}

	if want := "codeberg.org/itiquette/nanolinter"; got != want {
		t.Errorf("DefaultReleaseImageRepository() = %q, want %q", got, want)
	}
}

func TestRegistryHost_StripsSchemeAndPath(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ raw, want string }{
		"an https URL":             {raw: "https://codeberg.org", want: "codeberg.org"},
		"an http URL with a slash": {raw: "http://git.example.test/", want: "git.example.test"},
		"a repository path":        {raw: "registry.example.test/org/repo", want: "registry.example.test"},
		"surrounding whitespace":   {raw: "  codeberg.org  ", want: "codeberg.org"},
		"an explicit port":         {raw: "codeberg.org:5000", want: "codeberg.org:5000"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := appcontainer.RegistryHost(tc.raw)
			if err != nil {
				t.Fatalf("RegistryHost(%q): %v", tc.raw, err)
			}

			if got != tc.want {
				t.Errorf("RegistryHost(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestRegistryHost_RefusesWithoutEchoingCredentials covers the refusals, and
// the property two of them used to break: every refusal quoted the raw input,
// so a server URL misconfigured as https://user:TOKEN@host put the token into
// the error and from there into a CI log. The canary must not appear in any
// refusal, whichever of the three parse paths it reached.
func TestRegistryHost_RefusesWithoutEchoingCredentials(t *testing.T) {
	t.Parallel()

	const canary = "s3cr3t-canary"

	for name, raw := range map[string]string{
		"a password in a URL":         "https://user:" + canary + "@codeberg.org",
		"a token as the URL username": "https://" + canary + "@codeberg.org/org/repo",
		"userinfo on a bare host":     "user:" + canary + "@codeberg.org",
		"userinfo on a repository":    canary + "@codeberg.org/org/repo",
		"another scheme":              "ftp://codeberg.org",
		"a scheme with no host":       "https://",
		"whitespace only":             "   ",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := appcontainer.RegistryHost(raw)
			if !errors.Is(err, errs.ErrUsage) {
				t.Fatalf("RegistryHost(%q) = (%q, %v), want ErrUsage", raw, got, err)
			}

			if strings.Contains(err.Error(), canary) {
				t.Errorf("the refusal echoes the credential: %v", err)
			}

			// The wrapper names its context, so an operator can tell which of
			// several host inputs was refused.
			if !strings.HasPrefix(err.Error(), "release images: ") {
				t.Errorf("err = %v, want the release images prefix", err)
			}
		})
	}
}

// TestOptionalRegistryHost_OnlyEmptyIsOptional pins the wrapper's one rule:
// an absent value is allowed and yields no host, while any present value is
// validated in full. Whitespace is present, not absent -- a flag set to spaces
// is a mistake to report rather than an omission to accept.
func TestOptionalRegistryHost_OnlyEmptyIsOptional(t *testing.T) {
	t.Parallel()

	if got, err := appcontainer.OptionalRegistryHost(""); got != "" || err != nil {
		t.Errorf("empty = (%q, %v), want no host and no error", got, err)
	}

	if got, err := appcontainer.OptionalRegistryHost("   "); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("whitespace = (%q, %v), want ErrUsage", got, err)
	}

	if got, err := appcontainer.OptionalRegistryHost("https://codeberg.org/"); got != "codeberg.org" || err != nil {
		t.Errorf("present = (%q, %v), want codeberg.org", got, err)
	}
}

// TestNormalizedServerURL_KeepsTheSchemeIntact covers the normaliser feeding
// OptionalRegistryHost from --server-url, including the case it got wrong:
// "https://" had its scheme slashes trimmed into "https:", gained a second
// scheme, and reached the host parser as "https://https:", which it accepted as
// the registry "https:" with no error.
func TestNormalizedServerURL_KeepsTheSchemeIntact(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ raw, want string }{
		"a bare host gains https":      {raw: "codeberg.org", want: "https://codeberg.org"},
		"trailing slashes are trimmed": {raw: "https://codeberg.org//", want: "https://codeberg.org"},
		"an http scheme is kept":       {raw: " http://git.example.test/ ", want: "http://git.example.test"},
		"empty stays empty":            {raw: "", want: ""},
		"whitespace becomes empty":     {raw: "  ", want: ""},
		"a bare scheme stays bare":     {raw: "https://", want: "https://"},
		"a bare scheme with a slash":   {raw: "https:///", want: "https://"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := appcontainer.NormalizedServerURL(tc.raw); got != tc.want {
				t.Errorf("NormalizedServerURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}

	// And the composition the CLI performs refuses the bare scheme.
	if host, err := appcontainer.OptionalRegistryHost(appcontainer.NormalizedServerURL("https://")); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("--server-url https:// became host %q with err %v, want ErrUsage", host, err)
	}
}

func TestReleaseImagesReadBoundary_RejectsExternalLedger(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	require.NoError(t, os.Mkdir(dist, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "external.json"), []byte("[]"), 0o600))

	ledger := filepath.Join(dist, "ledger.json")
	require.NoError(t, os.Symlink(filepath.Join(root, "external.json"), ledger))

	_, _, err := appcontainer.LoadReleaseImagesLedger(appcontainer.ReleaseImagesLedgerInput{DistDir: dist, Ledger: ledger, ReleaseTag: "v1.0.0"})
	require.ErrorIs(t, err, errs.ErrValidation)
	require.NoError(t, os.Remove(ledger))
	require.NoError(t, os.WriteFile(ledger, []byte("[]"), 0o600))

	entries, present, err := appcontainer.LoadReleaseImagesLedger(appcontainer.ReleaseImagesLedgerInput{DistDir: dist, Ledger: ledger, ReleaseTag: "v1.0.0"})
	require.NoError(t, err)
	require.True(t, present)
	require.Empty(t, entries)
}

// Admission refuses a ledger a verb cannot act on before the caller acquires
// any registry or signing authority: an empty one where entries are required,
// and one whose images sit outside the expected repository.
func TestLoadReleaseImagesLedger_RefusesEmptyAndForeignLedgers(t *testing.T) {
	t.Parallel()

	const (
		repo   = "registry.example/owner/app"
		digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	dist := filepath.Join(t.TempDir(), "dist")
	require.NoError(t, os.Mkdir(dist, 0o700))

	body, err := imageledger.Marshal([]imageledger.Entry{{Ref: repo + "@" + digest, Digest: digest, Role: "app", FinalTag: repo + ":v1.0.0"}})
	require.NoError(t, err)

	filled := filepath.Join(dist, "filled.json")
	require.NoError(t, os.WriteFile(filled, body, 0o600))

	empty := filepath.Join(dist, "empty.json")
	require.NoError(t, os.WriteFile(empty, []byte("[]"), 0o600))

	in := appcontainer.ReleaseImagesLedgerInput{DistDir: dist, Ledger: filled, ReleaseTag: "v1.0.0", ExpectedImageRepository: repo, NonEmpty: true}

	entries, present, err := appcontainer.LoadReleaseImagesLedger(in)
	require.NoError(t, err)
	require.True(t, present)
	require.Len(t, entries, 1)

	foreign := in
	foreign.ExpectedImageRepository = "registry.example/owner/other"
	_, _, err = appcontainer.LoadReleaseImagesLedger(foreign)
	require.ErrorIs(t, err, errs.ErrValidation)
	require.ErrorContains(t, err, "entry 0")

	none := in
	none.Ledger = empty
	_, _, err = appcontainer.LoadReleaseImagesLedger(none)
	require.ErrorIs(t, err, errs.ErrValidation)
	require.ErrorContains(t, err, "at least one entry")

	absent := in
	absent.Ledger = filepath.Join(dist, "absent.json")
	_, _, err = appcontainer.LoadReleaseImagesLedger(absent)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorContains(t, err, "release images: read ledger")
}

// MayBeAbsent admits one thing: a default ledger that does not exist in a dist
// directory that does. A ledger that exists is refused exactly as it is
// without it, an empty one included, and a named ledger has to exist.
func TestLoadReleaseImagesLedger_MayBeAbsentAdmitsOnlyAnAbsentDefaultLedger(t *testing.T) {
	t.Parallel()

	const (
		repo   = "registry.example/owner/app"
		digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	filled, err := imageledger.Marshal([]imageledger.Entry{{Ref: repo + "@" + digest, Digest: digest, Role: "app", FinalTag: repo + ":v1.0.0"}})
	require.NoError(t, err)

	writeLedger := func(body string) func(*testing.T, string) {
		return func(t *testing.T, ledger string) {
			t.Helper()
			require.NoError(t, os.WriteFile(ledger, []byte(body), 0o600))
		}
	}

	for _, tc := range []struct {
		name     string
		explicit bool
		noDist   bool
		arrange  func(t *testing.T, ledger string)
		repo     string
		// off and on are the refusal without and with the caller's optional
		// request; nil means admitted.
		off, on error
		entries int // admitted entries; zero with no refusal is an absent ledger
	}{
		{name: "default absent", off: os.ErrNotExist},
		{name: "default absent without a dist directory", noDist: true, off: os.ErrNotExist, on: os.ErrNotExist},
		{name: "default empty array", arrange: writeLedger("[]"), off: errs.ErrValidation, on: errs.ErrValidation},
		{name: "default null", arrange: writeLedger("null"), off: errs.ErrMalformedInput, on: errs.ErrMalformedInput},
		{name: "default zero bytes", arrange: writeLedger(""), off: errs.ErrMalformedInput, on: errs.ErrMalformedInput},
		{name: "default whitespace", arrange: writeLedger(" \n\t"), off: errs.ErrMalformedInput, on: errs.ErrMalformedInput},
		{name: "explicit absent", explicit: true, off: os.ErrNotExist, on: os.ErrNotExist},
		{name: "explicit empty array", explicit: true, arrange: writeLedger("[]"), off: errs.ErrValidation, on: errs.ErrValidation},
		{
			name: "dangling symlink", off: errs.ErrValidation, on: errs.ErrValidation,
			arrange: func(t *testing.T, ledger string) {
				t.Helper()
				require.NoError(t, os.Symlink(filepath.Join(filepath.Dir(ledger), "gone.json"), ledger))
			},
		},
		{
			name: "non-regular file", off: errs.ErrValidation, on: errs.ErrValidation,
			arrange: func(t *testing.T, ledger string) {
				t.Helper()
				require.NoError(t, os.Mkdir(ledger, 0o700))
			},
		},
		{name: "malformed", arrange: writeLedger("{"), off: errs.ErrMalformedInput, on: errs.ErrMalformedInput},
		{name: "wrong release tag", arrange: writeLedger(strings.ReplaceAll(string(filled), "v1.0.0", "v9.9.9")), off: errs.ErrValidation, on: errs.ErrValidation},
		{name: "foreign repository", arrange: writeLedger(string(filled)), repo: "registry.example/owner/other", off: errs.ErrValidation, on: errs.ErrValidation},
		{name: "filled", arrange: writeLedger(string(filled)), entries: 1},
	} {
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/optional=%t", tc.name, optional), func(t *testing.T) {
				t.Parallel()

				dist := filepath.Join(t.TempDir(), "dist")
				if !tc.noDist {
					require.NoError(t, os.Mkdir(dist, 0o700))
				}

				ledger := filepath.Join(dist, "release-images.json")
				if tc.arrange != nil {
					tc.arrange(t, ledger)
				}

				expectedRepo := repo
				if tc.repo != "" {
					expectedRepo = tc.repo
				}

				entries, present, err := appcontainer.LoadReleaseImagesLedger(appcontainer.ReleaseImagesLedgerInput{
					DistDir: dist, Ledger: ledger, ReleaseTag: "v1.0.0", ExpectedImageRepository: expectedRepo,
					NonEmpty: true, MayBeAbsent: optional && !tc.explicit,
				})

				want := tc.off
				if optional {
					want = tc.on
				}

				if want != nil {
					require.ErrorIs(t, err, want)
					require.False(t, present)
					require.Empty(t, entries)

					return
				}

				require.NoError(t, err)
				require.Equal(t, tc.entries > 0, present)
				require.Len(t, entries, tc.entries)
			})
		}
	}
}
