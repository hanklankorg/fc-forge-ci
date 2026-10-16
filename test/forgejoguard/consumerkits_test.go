// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// consumerKit is one ecosystem example's Forgejo pair: the directory holding
// its pullrequest.yml and release.yml, and the build action both use.
type consumerKit struct {
	dir         string
	buildAction string
}

func (k consumerKit) release() string     { return k.dir + "/release.yml" }
func (k consumerKit) pullRequest() string { return k.dir + "/pullrequest.yml" }

// consumerKits are every consumer pair under examples/, each held to the
// same rules. TestConsumerKitsAreEveryForgejoExample keeps the list whole.
func consumerKits() []consumerKit {
	return []consumerKit{
		{dir: "examples/go-cli/forgejo", buildAction: "build-go"},
		{dir: "examples/go-service/forgejo", buildAction: "build-go"},
		{dir: "examples/cargo-app/forgejo", buildAction: "build-cargo"},
		{dir: "examples/maven-app/forgejo", buildAction: "build-maven"},
	}
}

// forgejoExampleTrees are the example directories a layer-wide scan reads:
// the organization's signer-image example and every consumer pair.
func forgejoExampleTrees() []string {
	kits := consumerKits()

	trees := make([]string, 0, 1+len(kits))
	trees = append(trees, "examples/forgejo")

	for _, kit := range kits {
		trees = append(trees, kit.dir)
	}

	return trees
}

// Every examples/**/forgejo/ directory with a release caller is a kit above,
// so a new ecosystem's pair cannot escape the consumer rules by being left
// off the list.
func TestConsumerKitsAreEveryForgejoExample(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)

	var found []string

	err := filepath.WalkDir(filepath.Join(root, "examples"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != "release.yml" || filepath.Base(filepath.Dir(path)) != "forgejo" {
			return err
		}

		relative, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}

		found = append(found, filepath.ToSlash(relative))

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	listed := make([]string, 0, len(consumerKits()))
	for _, kit := range consumerKits() {
		listed = append(listed, kit.dir)
	}

	slices.Sort(found)
	slices.Sort(listed)

	if !slices.Equal(found, listed) {
		t.Errorf("the Forgejo consumer pairs under examples/ are %v, but consumerKits lists %v", found, listed)
	}
}
