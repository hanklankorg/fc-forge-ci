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
	"reflect"
	"slices"
	"strings"
	"testing"

	appinit "github.com/diggsweden/forge-ci/internal/app/consumerinit"
	domain "github.com/diggsweden/forge-ci/internal/domain/consumerinit"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// fakeRemote serves this repository's own examples and changelog
// configurations as the pinned release, and the defaults file the case gives.
type fakeRemote struct {
	tags        []string
	defaults    string // empty: the mirror has no defaults file
	commit      string // empty: use the default SHA-1 fixture
	noChangelog bool   // an older release without templates/gitcliff
	fetchErr    error
}

func (f fakeRemote) VersionTags(context.Context, string) ([]string, error) { return f.tags, nil }

func (f fakeRemote) TagCommit(context.Context, string, string) (string, error) {
	if f.commit != "" {
		return f.commit, nil
	}

	return strings.Repeat("ab", 20), nil
}

func (f fakeRemote) Export(_ context.Context, _, _, dest string, _ []string) ([]string, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}

	if err := os.CopyFS(filepath.Join(dest, "examples"), os.DirFS(repoExamples)); err != nil {
		return nil, err
	}

	var missing []string
	if f.noChangelog {
		missing = append(missing, domain.ChangelogDir)
	} else if err := os.CopyFS(filepath.Join(dest, filepath.FromSlash(domain.ChangelogDir)), os.DirFS(repoChangelog)); err != nil {
		return nil, err
	}

	if f.defaults == "" {
		return append(missing, domain.DefaultsFile), nil
	}

	file := filepath.Join(dest, filepath.FromSlash(domain.DefaultsFile))
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return nil, err
	}

	return missing, os.WriteFile(file, []byte(f.defaults), 0o600)
}

// repoExamples and repoChangelog are this repository's examples and
// changelog configurations; go test runs in the package directory.
const (
	repoExamples  = "../../../examples"
	repoChangelog = "../../../templates/gitcliff"
)

const fullDefaults = `forge: %s
signer-image: registry.example/fixture/signer@sha256:` + "1111111111111111111111111111111111111111111111111111111111111111" + `
cosign-public-key-sha256: 2222222222222222222222222222222222222222222222222222222222222222
check-image: registry.example/fixture/check@sha256:3333333333333333333333333333333333333333333333333333333333333333
nanolinter-images:
  go: registry.example/fixture/lint:go@sha256:4444444444444444444444444444444444444444444444444444444444444444
  java: registry.example/fixture/lint:java@sha256:5555555555555555555555555555555555555555555555555555555555555555
  kotlin: registry.example/fixture/lint:kotlin@sha256:6666666666666666666666666666666666666666666666666666666666666666
  rust: registry.example/fixture/lint:rust@sha256:7777777777777777777777777777777777777777777777777777777777777777
  javascript-typescript: registry.example/fixture/lint:javascript-typescript@sha256:8888888888888888888888888888888888888888888888888888888888888888
runtime-images:
  java: registry.example/fixture/runtime-java@sha256:9999999999999999999999999999999999999999999999999999999999999999
  node: registry.example/fixture/runtime-node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  go: registry.example/fixture/runtime-go@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  rust: registry.example/fixture/runtime-rust@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
  android: registry.example/fixture/runtime-android@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
`

// With a complete defaults file, every example of every forge is written
// with only what a person must decide left over: the bot identity and the
// artifact's name. A placeholder the substitution table misses, or an
// example that grows a new one, fails here rather than in a consumer's run.
func TestRunLeavesOnlyThePersonsChoices(t *testing.T) {
	t.Parallel()

	mirrors := map[provider.ForgeAPI]string{
		provider.ForgeGitHub:  "https://github.com/fixture/delivery-engine",
		provider.ForgeForgejo: "https://forge.example/fixture/delivery-engine",
		provider.ForgeGitLab:  "https://gitlab.example/fixture/delivery-engine",
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

	dir := t.TempDir()

	result, err := appinit.Run(t.Context(), appinit.Input{Remote: remote, URL: mirror, Dir: dir, Example: example})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"nanolinter.toml", ".mega-linter.yml", ".mega-linter.yaml"} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("init generated linter config %s: %v", name, statErr)
		}
	}

	if result.Forge != forge || result.Source.Tag != "v0.91.0" {
		t.Fatalf("forge %s tag %s, want %s v0.91.0", result.Forge, result.Source.Tag, forge)
	}

	for file, left := range result.Remaining {
		for _, p := range left {
			// A GitHub consumer calls the upstream workflows with no mirror
			// defaults, so the lint image's digest is the person's to review.
			if forge == provider.ForgeGitHub && p.Token == "PIN_DIGEST" {
				continue
			}

			if !strings.HasPrefix(p.Token, "FIXME") && !slices.Contains([]string{
				"PIN_MAVEN_IMAGE_DIGEST", "PIN_RUST_IMAGE_DIGEST", "my-cli", "my-service", "my-app",
				"com.example.myapp", "com.example.myapp.demo",
			}, p.Token) {
				t.Errorf("%s:%d still holds %s", file, p.Line, p.Token)
			}
		}
	}

	if forge == provider.ForgeGitHub && example == "go-cli" {
		found := false
		for _, p := range result.Remaining[".forge-ci/artifacts.yml"] {
			found = found || p.Token == "my-cli"
		}

		if !found {
			t.Fatal("generated Go build names and paths must be reported for customization")
		}
	}
}

func TestRunFromRenamedForgejoForkPreservesFullPins(t *testing.T) {
	t.Parallel()

	for _, commit := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		t.Run(commit, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			remote := fakeRemote{
				tags: []string{"v1.2.3"}, commit: commit,
				defaults: strings.Replace(fullDefaults, "%s", "forgejo", 1),
			}

			result, err := appinit.Run(t.Context(), appinit.Input{
				Remote: remote, URL: "https://forge.example/fixture/delivery-engine", Dir: dir, Example: "go-cli",
			})
			if err != nil {
				t.Fatal(err)
			}

			if result.Source.Repository != "fixture/delivery-engine" || result.Source.Commit != commit {
				t.Fatalf("source identity changed: %+v", result.Source)
			}

			for _, name := range []string{"release.yml", "pullrequest.yml"} {
				body, readErr := os.ReadFile(filepath.Join(dir, ".forgejo", "workflows", name))
				if readErr != nil {
					t.Fatal(readErr)
				}

				text := string(body)
				if !strings.Contains(text, "fixture/delivery-engine/.forgejo/") || !strings.Contains(text, "@"+commit) {
					t.Fatalf("%s lost the renamed repository or full pin", name)
				}

				for _, stale := range []string{"OWNER/forge-ci", "PIN_FORGE_CI_SHA", "FORGE_HOST", "diggsweden/forge-ci"} {
					if strings.Contains(text, stale) {
						t.Errorf("%s retains source placeholder %q", name, stale)
					}
				}
			}
		})
	}
}

// Forgejo and GitLab releases read the consumer's changelog configuration
// and stop without it, so init writes the release's; GitHub's release brings
// its own. Only workflows go to .forgejo/workflows: the go-service Forgejo
// directory also holds a Containerfile.example the README places elsewhere.
func TestRunWritesWhatTheForgesReleaseReads(t *testing.T) {
	t.Parallel()

	changelog := []string{".forge-ci/gitcliff/keepachangelog.toml", ".forge-ci/gitcliff/minimal.toml"}
	cases := map[provider.ForgeAPI]struct {
		mirror string
		want   []string
	}{
		provider.ForgeGitHub: {"https://github.com/fixture/forge-ci", []string{
			".forge-ci/artifacts.yml", ".github/workflows/pullrequest-workflow.yml", ".github/workflows/release-workflow.yml",
		}},
		provider.ForgeForgejo: {"https://forge.example/fixture/forge-ci", append([]string{
			".forgejo/workflows/pullrequest.yml", ".forgejo/workflows/release.yml",
		}, changelog...)},
		provider.ForgeGitLab: {"https://gitlab.example/fixture/forge-ci", append([]string{".gitlab-ci.yml"}, changelog...)},
	}

	for forge, tc := range cases {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()

			remote := fakeRemote{tags: []string{"v1.2.3"}}
			if forge != provider.ForgeGitHub {
				remote.defaults = "forge: " + string(forge) + "\n"
			}

			dir := t.TempDir()

			result, err := appinit.Run(t.Context(), appinit.Input{Remote: remote, URL: tc.mirror, Dir: dir, Example: "go-service"})
			if err != nil {
				t.Fatal(err)
			}

			if got := slices.Sorted(slices.Values(result.Written)); !slices.Equal(got, slices.Sorted(slices.Values(tc.want))) {
				t.Fatalf("wrote %v, want %v", got, tc.want)
			}

			for _, file := range changelog {
				if !slices.Contains(tc.want, file) {
					continue
				}

				got, readErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
				if readErr != nil {
					t.Fatal(readErr)
				}

				want, readErr := os.ReadFile(filepath.Join(repoChangelog, path.Base(file)))
				if readErr != nil {
					t.Fatal(readErr)
				}

				if string(got) != string(want) {
					t.Errorf("%s differs from the release's templates/gitcliff", file)
				}
			}
		})
	}
}

// The changelog configuration may already be there: copied by hand, as the
// guides said before init wrote it, or by init for the repository's other
// forge. Init keeps it and writes the rest instead of refusing.
func TestRunKeepsExistingChangelogConfiguration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mine := filepath.Join(dir, ".forge-ci", "gitcliff", "minimal.toml")

	if err := os.MkdirAll(filepath.Dir(mine), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(mine, []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	remote := fakeRemote{tags: []string{"v1.2.3"}, defaults: "forge: gitlab\n"}

	result, err := appinit.Run(t.Context(), appinit.Input{Remote: remote, URL: "https://gitlab.example/fixture/forge-ci", Dir: dir, Example: "go-cli"})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{".gitlab-ci.yml", ".forge-ci/gitcliff/keepachangelog.toml"}; !slices.Equal(result.Written, want) {
		t.Fatalf("wrote %v, want %v", result.Written, want)
	}

	if !slices.Equal(result.Kept, []string{".forge-ci/gitcliff/minimal.toml"}) || len(result.Conflicts) != 0 {
		t.Fatalf("kept %v, conflicts %v", result.Kept, result.Conflicts)
	}

	if body, readErr := os.ReadFile(mine); readErr != nil || string(body) != "# mine\n" {
		t.Fatalf("init changed the consumer's own configuration: %q, %v", body, readErr)
	}
}

// A release from before init wrote changelog configuration still
// initializes, with the files it has.
func TestRunFromAReleaseWithoutChangelogConfiguration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	remote := fakeRemote{tags: []string{"v1.2.3"}, defaults: "forge: gitlab\n", noChangelog: true}

	result, err := appinit.Run(t.Context(), appinit.Input{Remote: remote, URL: "https://gitlab.example/fixture/forge-ci", Dir: dir, Example: "go-cli"})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(result.Written, []string{".gitlab-ci.yml"}) {
		t.Fatalf("wrote %v, want only .gitlab-ci.yml", result.Written)
	}
}

func TestRunWritesNothingWhenATargetExists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forge-ci"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, ".forge-ci", "artifacts.yml"), []byte("# mine\n"), 0o600); err != nil {
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

func TestRunPreservesConsumerLinterConfigs(t *testing.T) {
	t.Parallel()

	for _, forge := range []string{"github", "gitlab", "forgejo"} {
		t.Run(forge, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			configs := []string{"nanolinter.toml", ".mega-linter.yml", ".mega-linter.yaml"}

			for _, name := range configs {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("# consumer-owned "+name+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			_, err := appinit.Run(t.Context(), appinit.Input{
				Remote: fakeRemote{tags: []string{"v0.90.0"}, defaults: strings.Replace(fullDefaults, "%s", forge, 1)},
				URL:    "https://" + forge + ".com/fixture/forge-ci", Dir: dir, Example: "go-cli",
			})
			if err != nil {
				t.Fatal(err)
			}

			for _, name := range configs {
				body, readErr := os.ReadFile(filepath.Join(dir, name))
				if readErr != nil || string(body) != "# consumer-owned "+name+"\n" {
					t.Errorf("consumer config %s changed: %q, %v", name, body, readErr)
				}
			}
		})
	}
}

func TestResolveSourceRefusesWhatItCannotPin(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		url    string
		remote fakeRemote
	}{
		"not https":                 {url: "http://github.com/diggsweden/forge-ci", remote: fakeRemote{tags: []string{"v1.0.0"}}},
		"malformed URL":             {url: "https://%", remote: fakeRemote{tags: []string{"v1.0.0"}}},
		"no repository path":        {url: "https://github.com/forge-ci", remote: fakeRemote{tags: []string{"v1.0.0"}}},
		"no release tag":            {url: "https://github.com/diggsweden/forge-ci", remote: fakeRemote{tags: []string{"v1.0.0-rc.1"}}},
		"mirror without forge":      {url: "https://codefloe.com/itiquette/forge-ci", remote: fakeRemote{tags: []string{"v1.0.0"}}},
		"defaults with unknown key": {url: "https://codefloe.com/itiquette/forge-ci", remote: fakeRemote{tags: []string{"v1.0.0"}, defaults: "forge: forgejo\nsigner: x\n"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, dryRun := range []bool{false, true} {
				if _, err := appinit.Run(t.Context(), appinit.Input{Remote: tc.remote, URL: tc.url, Dir: t.TempDir(), Example: "go-cli", DryRun: dryRun}); err == nil {
					t.Fatalf("dry-run=%t: resolved, want a refusal", dryRun)
				}
			}
		})
	}
}

func TestRunDryRunMatchesGeneratedProposal(t *testing.T) {
	t.Parallel()

	for _, forge := range []string{"github", "gitlab", "forgejo"} {
		t.Run(forge, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(t.TempDir(), "new project")
			input := appinit.Input{
				Remote: fakeRemote{tags: []string{"v1.2.3"}, defaults: strings.Replace(fullDefaults, "%s", forge, 1)},
				URL:    "https://" + forge + ".com/fixture/forge-ci", Dir: dir, Example: "go-cli", DryRun: true,
			}

			preview, err := appinit.Run(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}

			if !preview.DryRun || preview.Directory != dir || len(preview.Proposed) == 0 || len(preview.Written) != 0 || len(preview.Conflicts) != 0 {
				t.Fatalf("unexpected preview: %+v", preview)
			}

			if _, statErr := os.Lstat(dir); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("preview created the project directory: %v", statErr)
			}

			input.DryRun = false

			written, err := appinit.Run(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}

			if written.DryRun || !slices.Equal(preview.Proposed, written.Written) || preview.Source != written.Source || !reflect.DeepEqual(preview.Remaining, written.Remaining) {
				t.Fatalf("preview and generation differ: preview=%+v written=%+v", preview, written)
			}

			for _, file := range preview.Proposed {
				body, readErr := os.ReadFile(filepath.Join(dir, file))
				if readErr != nil {
					t.Fatal(readErr)
				}

				if !reflect.DeepEqual(domain.Remaining(string(body)), preview.Remaining[file]) {
					t.Errorf("preview placeholders differ from written content: %s", file)
				}
			}
		})
	}
}

func TestRunDryRunReportsAllConflictsWithoutChangingFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	workflow := ".github/workflows/pullrequest-workflow.yml"

	for _, name := range []string{".forge-ci/artifacts.yml", workflow, "nanolinter.toml", ".mega-linter.yml", "KEEP"} {
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(file, []byte("original: "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	before := snapshotProject(t, dir)

	result, err := appinit.Run(t.Context(), appinit.Input{
		Remote: fakeRemote{tags: []string{"v1.2.3"}}, URL: "https://github.com/fixture/forge-ci",
		Dir: dir, Example: "go-cli", DryRun: true,
	})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err=%v, want existing-file refusal", err)
	}

	if !slices.Equal(result.Conflicts, []string{".forge-ci/artifacts.yml", workflow}) || len(result.Proposed) <= len(result.Conflicts) || len(result.Written) != 0 {
		t.Fatalf("incomplete conflict/proposal report: %+v", result)
	}

	if len(result.Remaining) == 0 {
		t.Fatal("conflicts must not hide remaining placeholders")
	}

	if after := snapshotProject(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("preview changed the project: before=%v after=%v", before, after)
	}
}

func TestRunDryRunErrorsDoNotLookLikeEmptySuccess(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, example string
		remote        fakeRemote
	}{
		{name: "fetch_failure", example: "go-cli", remote: fakeRemote{tags: []string{"v1.2.3"}, fetchErr: fs.ErrPermission}},
		{name: "missing_example", example: "missing-example", remote: fakeRemote{tags: []string{"v1.2.3"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			before := snapshotProject(t, dir)

			result, err := appinit.Run(t.Context(), appinit.Input{
				Remote: tc.remote, URL: "https://github.com/fixture/forge-ci", Dir: dir, Example: tc.example, DryRun: true,
			})
			if err == nil || len(result.Written) != 0 || len(result.Proposed) != 0 {
				t.Fatalf("failed preview returned success/proposed files: %+v, %v", result, err)
			}

			if after := snapshotProject(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatal("failed preview changed the project")
			}
		})
	}
}

type projectEntry struct {
	mode fs.FileMode
	body string
}

type emptyRelease struct{ fakeRemote }

func (emptyRelease) Export(_ context.Context, _, _, dest string, _ []string) ([]string, error) {
	if err := os.MkdirAll(filepath.Join(dest, "examples", "go-cli", "github"), 0o750); err != nil {
		return nil, err
	}

	return []string{domain.DefaultsFile}, nil
}

func TestRunDryRunRefusesEmptyExample(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	result, err := appinit.Run(t.Context(), appinit.Input{
		Remote: emptyRelease{fakeRemote{tags: []string{"v1.2.3"}}}, URL: "https://github.com/fixture/forge-ci",
		Dir: dir, Example: "go-cli", DryRun: true,
	})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "no initialization files") || len(result.Written) != 0 {
		t.Fatalf("empty release must be refused: %+v, %v", result, err)
	}

	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed preview changed target: %v, %v", entries, readErr)
	}
}

func snapshotProject(t *testing.T, dir string) map[string]projectEntry {
	t.Helper()

	entries := map[string]projectEntry{}
	project := os.DirFS(dir)

	err := fs.WalkDir(project, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		var body []byte

		if !entry.IsDir() {
			body, err = fs.ReadFile(project, file)
			if err != nil {
				return err
			}
		}

		entries[file] = projectEntry{mode: info.Mode(), body: string(body)}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return entries
}

type unreachableRemote struct{ fakeRemote }

func (unreachableRemote) VersionTags(context.Context, string) ([]string, error) {
	return nil, errors.New("git ls-remote: exit status 128")
}

// A URL git cannot read says what it has to be: off github.com, the
// organization's mirror. The cause stays wrapped.
func TestRunSaysWhatTheURLMustBeWhenItCannotBeRead(t *testing.T) {
	t.Parallel()

	_, err := appinit.Run(t.Context(), appinit.Input{
		Remote: unreachableRemote{}, URL: "https://forge.example/org/forge-ci", Dir: t.TempDir(), Example: "go-cli",
	})
	if err == nil || !strings.Contains(err.Error(), "Forge CI mirror") || !strings.Contains(err.Error(), "exit status 128") {
		t.Fatalf("err = %v", err)
	}
}
