// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// canonicalImagePin is the one shape a digest-pinned image input of the layer
// takes: a complete repository coordinate, an optional tag, and the digest
// that decides what is pulled. The tag is what lets Renovate keep a pin of a
// public image current; the runtime pulls by the digest whatever it says.
// Each validating step holds a copy, because it runs before the layer's
// helpers are fetched; this guard keeps every copy this one.
const canonicalImagePin = `^[a-z0-9]+([.-][a-z0-9]+)*(:[0-9]+)?/([a-z0-9]+(([._]|__|-+)[a-z0-9]+)*/)*` +
	`[a-z0-9]+(([._]|__|-+)[a-z0-9]+)*(:` + container.OCITagComponent + `)?@sha256:[0-9a-f]{64}$`

// imagePinCheck finds a bash test of a value against a digest-pinned pattern.
var imagePinCheck = regexp.MustCompile(`=~ (\^\S*@sha256:\[0-9a-f\]\{64\}\$)`)

func TestEveryImagePinCheckIsTheCanonicalOne(t *testing.T) {
	t.Parallel()

	root := filepath.Join(reporoot.Path(t), ".forgejo")
	checks := 0

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || (filepath.Ext(path) != ".yml" && filepath.Ext(path) != ".yaml") {
			return walkErr
		}

		body, err := os.ReadFile(path) //nolint:gosec // a file of this repository's own layer.
		if err != nil {
			return err
		}

		for _, match := range imagePinCheck.FindAllStringSubmatch(string(body), -1) {
			checks++

			if match[1] != canonicalImagePin {
				t.Errorf("%s checks an image pin with %s, want the canonical %s", strings.TrimPrefix(path, root), match[1], canonicalImagePin)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if checks == 0 {
		t.Fatal("found no image pin check; the guard no longer sees the workflows")
	}
}

func TestCanonicalImagePinAcceptsDigestPinsOnly(t *testing.T) {
	t.Parallel()

	pin := regexp.MustCompile(canonicalImagePin)
	digest := "@sha256:" + strings.Repeat("a", 64)

	for image, accepted := range map[string]bool{
		"docker.io/library/maven" + digest:                         true,
		"docker.io/library/maven:3.9-eclipse-temurin-25" + digest:  true,
		"forgejo.example:8443/org/forge-ci-signer" + digest:        true,
		"forgejo.example:8443/org/forge-ci-signer:v1.2.3" + digest: true,
		"docker.io/library/maven:3.9":                              false,
		"maven" + digest:                                           false,
		"docker.io/library/maven:" + digest:                        false,
		"docker.io/library/maven@sha256:abc":                       false,
		"docker.io/library/maven:-tag" + digest:                    false,
	} {
		if pin.MatchString(image) != accepted {
			t.Errorf("%s: accepted %v, want %v", image, !accepted, accepted)
		}
	}
}
