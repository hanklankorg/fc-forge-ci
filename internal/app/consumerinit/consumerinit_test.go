// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package consumerinit_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	appinit "github.com/diggsweden/forge-ci/internal/app/consumerinit"
	domain "github.com/diggsweden/forge-ci/internal/domain/consumerinit"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// fakeRemote serves this repository's own examples as the pinned release,
// and the defaults file the case gives.
type fakeRemote struct {
	tags     []string
	defaults string // empty: the mirror has no defaults file
}

func (f fakeRemote) VersionTags(context.Context, string) ([]string, error) { return f.tags, nil }

func (fakeRemote) TagCommit(context.Context, string, string) (string, error) {
	return strings.Repeat("ab", 20), nil
}

func (f fakeRemote) Export(_ context.Context, _, _, dest string, _ []string) ([]string, error) {
	if err := os.CopyFS(filepath.Join(dest, "examples"), os.DirFS(repoExamples)); err != nil {
		return nil, err
	}

	if f.defaults == "" {
		return []string{domain.DefaultsFile}, nil
	}

	file := filepath.Join(dest, filepath.FromSlash(domain.DefaultsFile))
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return nil, err
	}

	return nil, os.WriteFile(file, []byte(f.defaults), 0o600)
}

// repoExamples is this repository's examples directory; go test runs in
// the package directory.
const repoExamples = "../../../examples"

const fullDefaults = `forge: %s
signer-image: codefloe.com/acme/forge-ci-signer@sha256:` + "1111111111111111111111111111111111111111111111111111111111111111" + `
cosign-public-key-sha256: 2222222222222222222222222222222222222222222222222222222222222222
check-image: registry.acme.se/ci/check@sha256:3333333333333333333333333333333333333333333333333333333333333333
nanolinter-images:
  go: ghcr.io/itiquette/nanolinter:go@sha256:4444444444444444444444444444444444444444444444444444444444444444
  java: ghcr.io/itiquette/nanolinter:java@sha256:5555555555555555555555555555555555555555555555555555555555555555
  kotlin: ghcr.io/itiquette/nanolinter:kotlin@sha256:6666666666666666666666666666666666666666666666666666666666666666
  rust: ghcr.io/itiquette/nanolinter:rust@sha256:7777777777777777777777777777777777777777777777777777777777777777
  javascript-typescript: ghcr.io/itiquette/nanolinter:javascript-typescript@sha256:8888888888888888888888888888888888888888888888888888888888888888
`

// With a complete defaults file, every example of every forge is written
// with only what a person must decide left over: the bot identity and the
// artifact's name. A placeholder the substitution table misses, or an
// example that grows a new one, fails here rather than in a consumer's run.
func TestRunLeavesOnlyThePersonsChoices(t *testing.T) {
	t.Parallel()

	mirrors := map[provider.ForgeAPI]string{
		provider.ForgeGitHub:  "https://github.com/diggsweden/forge-ci",
		provider.ForgeForgejo: "https://codefloe.com/itiquette/forge-ci",
		provider.ForgeGitLab:  "https://gitlab.acme.se/ci/forge-ci",
	}

	for forge, mirror := range mirrors {
		for _, example := range domain.Examples() {
			if _, err := fs.Stat(os.DirFS(repoExamples), path.Join(example, domain.ForgeDir(forge))); err != nil {
				continue
			}

			t.Run(string(forge)+"/"+example, func(t *testing.T) {
				t.Parallel()
				checkFilled(t, forge, mirror, example)
			})
		}
	}
}

func checkFilled(t *testing.T, forge provider.ForgeAPI, mirror, example string) {
	t.Helper()

	remote := fakeRemote{tags: []string{"v0.89.0", "v0.91.0", "v0.91.0-rc.1"}}
	if forge != provider.ForgeGitHub {
		remote.defaults = strings.Replace(fullDefaults, "%s", string(forge), 1)
	}

	result, err := appinit.Run(t.Context(), appinit.Input{Remote: remote, URL: mirror, Dir: t.TempDir(), Example: example})
	if err != nil {
		t.Fatal(err)
	}

	if result.Forge != forge || result.Source.Tag != "v0.91.0" {
		t.Fatalf("forge %s tag %s, want %s v0.91.0", result.Forge, result.Source.Tag, forge)
	}

	for file, left := range result.Remaining {
		for _, p := range left {
			if !strings.HasPrefix(p.Token, "FIXME") && p.Token != "PIN_MAVEN_IMAGE_DIGEST" {
				t.Errorf("%s:%d still holds %s", file, p.Line, p.Token)
			}
		}
	}
}

func TestRunWritesNothingWhenATargetExists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nanolinter.toml"), []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := appinit.Run(t.Context(), appinit.Input{
		Remote: fakeRemote{tags: []string{"v0.90.0"}},
		URL:    "https://github.com/diggsweden/forge-ci", Dir: dir, Example: "go-cli",
	})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err = %v, want a usage error", err)
	}

	if _, statErr := os.Stat(filepath.Join(dir, ".github")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatal("init wrote files although a target existed")
	}
}

func TestResolveSourceRefusesWhatItCannotPin(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		url    string
		remote fakeRemote
	}{
		"not https":                 {url: "http://github.com/diggsweden/forge-ci", remote: fakeRemote{tags: []string{"v1.0.0"}}},
		"no repository path":        {url: "https://github.com/forge-ci", remote: fakeRemote{tags: []string{"v1.0.0"}}},
		"no release tag":            {url: "https://github.com/diggsweden/forge-ci", remote: fakeRemote{tags: []string{"v1.0.0-rc.1"}}},
		"mirror without forge":      {url: "https://codefloe.com/itiquette/forge-ci", remote: fakeRemote{tags: []string{"v1.0.0"}}},
		"defaults with unknown key": {url: "https://codefloe.com/itiquette/forge-ci", remote: fakeRemote{tags: []string{"v1.0.0"}, defaults: "forge: forgejo\nsigner: x\n"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := appinit.Run(t.Context(), appinit.Input{Remote: tc.remote, URL: tc.url, Dir: t.TempDir(), Example: "go-cli"}); err == nil {
				t.Fatal("resolved, want a refusal")
			}
		})
	}
}
