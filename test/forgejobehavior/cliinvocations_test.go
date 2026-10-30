// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

var (
	// forgeCIContinuation joins a shell line continuation.
	forgeCIContinuation = regexp.MustCompile(`\\\n[ \t]*`)
	// forgeCICall matches a forge-ci call through the resolver function,
	// the binary path, a PATH call at the start of a line, the verified
	// signing executable, the GitLab release wrapper, or the installed binary
	// the lint jobs name RC_BIN, and captures the rest of the command. A PATH
	// call may follow a GitHub `run:` key, a GitLab script list dash or one
	// quoted variable assignment.
	forgeCICall = regexp.MustCompile(`(?m)(?:run_forge_ci|rc_engine|/forge-ci"?|^[ \t]*(?:run:[ \t]+|-[ \t]+|(?:[A-Z_]+="[^"\n]*"[ \t]+)+)?(?:! )?forge-ci|"\$\{SIGNING_EXECUTABLE:\?\}"|"\$RC_BIN")[ \t]+([a-z][^;|&\n]*)`)
	// forgeCIVariableCall is a call through another variable, which the
	// extraction cannot see.
	forgeCIVariableCall = regexp.MustCompile(`"\$\{[A-Za-z_]*[Bb]in[A-Za-z_]*\}"[ \t]+[a-z]`)
	forgeCIVerbs        = regexp.MustCompile(`^[a-z][a-z0-9 -]*`)
	// forgeCINested is text whose options belong to another command: a
	// single-quoted string or a command substitution.
	forgeCINested = regexp.MustCompile(`'[^']*'|\$\([^)]*\)`)
	forgeCIFlag   = regexp.MustCompile(`(?:^|\s)(--[a-z][a-z0-9-]*)`)
	forgeCIArray  = regexp.MustCompile(`"\$\{([A-Za-z_][A-Za-z0-9_]*)\[@\]\}"`)
)

// forgeExampleDirs are the Forgejo and GitLab example directories under
// examples/: examples/forgejo and every ecosystem's forgejo/ and gitlab/.
func forgeExampleDirs(t *testing.T) []string {
	t.Helper()
	root := reporoot.Path(t)

	var dirs []string

	err := filepath.WalkDir(filepath.Join(root, "examples"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() || (entry.Name() != "forgejo" && entry.Name() != "gitlab") {
			return err
		}

		relative, err := filepath.Rel(root, path)
		dirs = append(dirs, filepath.ToSlash(relative))

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(dirs) == 0 {
		t.Fatal("found no Forgejo or GitLab example directory")
	}

	return dirs
}

// forgeCIInvocation is one forge-ci command this repository runs.
type forgeCIInvocation struct {
	file  string
	verbs string   // space-separated verb path, such as "release files manifest"
	flags []string // long options passed, sorted and unique
}

// forgeCIInvocations extracts every forge-ci call from the Forgejo
// workflows, release scripts and actions, the GitHub workflows and the GitLab
// components and release scripts. Options passed through an array expansion
// are read from that array's assignments before the call in the same file.
// An empty assignment starts the array afresh, so two workflow steps may each
// build one of the same name; any other assignment adds, so both arms of a branch count.
func forgeCIInvocations(t *testing.T) []forgeCIInvocation {
	t.Helper()
	root := reporoot.Path(t)

	var invocations []forgeCIInvocation

	exampleDirs := forgeExampleDirs(t)
	dirs := make([]string, 0, 6+len(exampleDirs))
	dirs = append(dirs, ".forgejo/workflows", ".forgejo/scripts", ".forgejo/actions", ".github/workflows", "templates", ".gitlab/bin")
	dirs = append(dirs, exampleDirs...)

	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), "test-") {
				return err
			}

			relative, _ := filepath.Rel(root, path)

			raw := string(reporoot.ReadFile(t, relative))
			// A GitLab component's first document declares its inputs and runs
			// nothing; its prose can start a line with the name.
			if _, jobs, found := strings.Cut(raw, "\n---\n"); found && dir == "templates" {
				raw = jobs
			}

			if call := forgeCIVariableCall.FindString(raw); call != "" {
				t.Errorf("%s runs forge-ci through a variable the contract cannot see: %s", relative, call)
			}

			source := forgeCIContinuation.ReplaceAllString(raw, " ")
			for _, call := range forgeCICall.FindAllStringSubmatchIndex(source, -1) {
				command := source[call[2]:call[3]]
				verbs, _, _ := strings.Cut(forgeCIVerbs.FindString(command), " -")

				arguments := forgeCINested.ReplaceAllString(command, "")
				// A literal end-of-options delimiter transfers subsequent flags
				// to the child command, for example policy nanolinter -- docker.
				arguments, _, _ = strings.Cut(arguments, " -- ")
				arguments += arrayOptions(source[:call[0]], arguments)

				var flags []string
				for _, flag := range forgeCIFlag.FindAllStringSubmatch(arguments, -1) {
					flags = append(flags, flag[1])
				}

				slices.Sort(flags)
				invocations = append(invocations, forgeCIInvocation{
					file: relative, verbs: strings.TrimSpace(verbs), flags: slices.Compact(flags),
				})
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	return invocations
}

// arrayOptions returns what the arrays expanded in arguments were given
// before the call. An empty assignment starts an array afresh; any other adds.
func arrayOptions(before, arguments string) string {
	var options strings.Builder

	for _, array := range forgeCIArray.FindAllStringSubmatch(arguments, -1) {
		assignment := regexp.MustCompile(`\b` + array[1] + `\+?=\(([^)]*)\)`)

		var values strings.Builder

		for _, match := range assignment.FindAllStringSubmatch(before, -1) {
			if match[1] == "" {
				values.Reset()
			}

			values.WriteString(" " + match[1])
		}

		options.WriteString(values.String())
	}

	return options.String()
}

// The extraction the engine-tagged CLI contract relies on still sees the
// repository's calls, including options built in an array and the call
// through the verified signing executable, and no call hides behind a
// variable. The contract itself runs the engine, so it is engine-tagged.
func TestForgeCIInvocationsAreExtractable(t *testing.T) {
	t.Parallel()

	verbs := map[string][]string{}

	flags := 0
	for _, invocation := range forgeCIInvocations(t) {
		verbs[invocation.verbs] = append(verbs[invocation.verbs], invocation.flags...)
		flags += len(invocation.flags)
	}

	if len(verbs) < 120 || flags < 350 {
		t.Fatalf("extracted %d verb paths and %d options; the extraction has drifted", len(verbs), flags)
	}

	for verb, flag := range map[string]string{
		"validate isolation":               "--signing-secret",
		"version commit-changelog-release": "--git-url",
		"container setup-buildah":          "--extra-packages",
		"container ledger promote":         "--stage",                  // a GitHub workflow
		"security report to-gitlab-sast":   "--output",                 // a GitLab component
		"container release-images import":  "--destination-repository", // the GitLab release script
	} {
		if !slices.Contains(verbs[verb], flag) {
			t.Errorf("forge-ci %s: extracted options %q lack %s", verb, verbs[verb], flag)
		}
	}
}
