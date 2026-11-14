// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

// baseImageInventory is the release file a base image graph publishes.
const baseImageInventory = "image-inventory.json"

// baseInventoryImage is one image an inventory lists: its name, the digest
// reference the release claims for it, and the final tag a base image of that
// name and input ID is promoted to, <input ID>-<name>.
type baseInventoryImage struct {
	Flavor, Ref, Tag string
}

// declaredBaseImages are the images a base image graph declares: each
// images/<name>/Containerfile among the paths of its prepared commit.
func declaredBaseImages(paths string) []string {
	var names []string

	for file := range strings.FieldsSeq(paths) {
		parts := strings.Split(file, "/")
		if len(parts) == 3 && parts[0] == "images" && parts[2] == "Containerfile" {
			names = append(names, parts[1])
		}
	}

	slices.Sort(names)

	return names
}

// baseInventoryImages holds a published inventory to the declaration: it
// lists each declared image once and no other, each by a digest reference in
// the one repository and with its input ID. The images are returned, each
// name with its own reference, for the registry to answer for; a problem is a
// claim the release makes and the declaration does not.
func baseInventoryImages(inventory []byte, declared []string, repository string) ([]baseInventoryImage, []string) {
	var stages map[string][]struct {
		Flavor      string `json:"flavor"`
		Ref         string `json:"ref"`
		BaseInputID string `json:"base_input_id"`
	}

	if err := json.Unmarshal(inventory, &stages); err != nil {
		return nil, []string{fmt.Sprintf("%s is not an inventory of base image stages: %v", baseImageInventory, err)}
	}

	var (
		listing  []baseInventoryImage
		problems []string
		listed   = map[string]int{}
	)

	for _, images := range stages {
		for _, image := range images {
			listed[image.Flavor]++

			digest, pinned := strings.CutPrefix(image.Ref, repository+"@sha256:")
			if !pinned || !container.ValidSHA256Hex(digest) {
				problems = append(problems, fmt.Sprintf("image %q is listed as %q, not by a digest in %s", image.Flavor, image.Ref, repository))

				continue
			}

			if !container.ValidSHA256Hex(image.BaseInputID) {
				problems = append(problems, fmt.Sprintf("image %q is listed with input ID %q, not a SHA-256", image.Flavor, image.BaseInputID))

				continue
			}

			listing = append(listing, baseInventoryImage{Flavor: image.Flavor, Ref: image.Ref, Tag: image.BaseInputID + "-" + image.Flavor})
		}
	}

	for _, name := range declared {
		if listed[name] != 1 {
			problems = append(problems, fmt.Sprintf("declared image %q is listed %d times, not once", name, listed[name]))
		}

		delete(listed, name)
	}

	for name := range listed {
		problems = append(problems, fmt.Sprintf("image %q is listed and not declared", name))
	}

	slices.SortFunc(listing, func(a, b baseInventoryImage) int { return strings.Compare(a.Flavor+a.Ref, b.Flavor+b.Ref) })
	slices.Sort(problems)

	return listing, problems
}

func TestBaseInventoryListsExactlyTheDeclaredImages(t *testing.T) {
	t.Parallel()

	const repository = "registry.example/owner/app-base"

	declared := declaredBaseImages("README.md\nbase-plan.sh\nimages/base/Containerfile\nimages/base/base.txt\nimages/tools/Containerfile\nimages/Containerfile\nother/x/Containerfile\nother/images/x/Containerfile\n")
	if !slices.Equal(declared, []string{"base", "tools"}) {
		t.Fatalf("declared images = %v", declared)
	}

	ref := func(digit string) string { return repository + "@sha256:" + strings.Repeat(digit, 64) }
	id := strings.Repeat("1", 64)
	entry := func(flavor, ref string) string {
		return fmt.Sprintf(`{"flavor":%q,"ref":%q,"base_input_id":%q}`, flavor, ref, id)
	}
	inventory := func(base, flavors string) []byte {
		return []byte(`{"base":[` + base + `],"flavors":[` + flavors + `]}`)
	}

	for _, tc := range []struct {
		name      string
		inventory []byte
		listed    int
		problem   string
	}{
		{name: "declared", inventory: inventory(entry("base", ref("a")), entry("tools", ref("b"))), listed: 2},
		{name: "missing", inventory: inventory(entry("base", ref("a")), ""), listed: 1, problem: `declared image "tools" is listed 0 times`},
		{name: "extra", inventory: inventory(entry("base", ref("a")), entry("tools", ref("b"))+","+entry("debug", ref("c"))), listed: 3, problem: `image "debug" is listed and not declared`},
		{name: "twice", inventory: inventory(entry("base", ref("a")), entry("tools", ref("b"))+","+entry("tools", ref("c"))), listed: 3, problem: `declared image "tools" is listed 2 times`},
		{name: "tag", inventory: inventory(entry("base", ref("a")), entry("tools", repository+":v1.0.0-tools")), listed: 1, problem: `not by a digest in ` + repository},
		{name: "other repository", inventory: inventory(entry("base", ref("a")), entry("tools", "registry.example/other@sha256:"+strings.Repeat("b", 64))), listed: 1, problem: `not by a digest in ` + repository},
		{name: "no input ID", inventory: inventory(entry("base", ref("a")), `{"flavor":"tools","ref":"`+ref("b")+`"}`), listed: 1, problem: `image "tools" is listed with input ID ""`},
		{name: "not an inventory", inventory: []byte(`[]`), problem: "is not an inventory"},
	} {
		listing, problems := baseInventoryImages(tc.inventory, declared, repository)

		if len(listing) != tc.listed {
			t.Errorf("%s: listing = %v, want %d", tc.name, listing, tc.listed)
		}

		if tc.name == "declared" && !slices.Equal(listing, []baseInventoryImage{{"base", ref("a"), id + "-base"}, {"tools", ref("b"), id + "-tools"}}) {
			t.Errorf("each name must carry its own reference and tag: %v", listing)
		}

		if tc.problem == "" && len(problems) != 0 || tc.problem != "" && (len(problems) != 1 || !strings.Contains(problems[0], tc.problem)) {
			t.Errorf("%s: problems = %v, want %q", tc.name, problems, tc.problem)
		}
	}
}
