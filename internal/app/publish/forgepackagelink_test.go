// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// linkingForge is a forge that keeps packages under their owner: it resolves
// the registries and records what it is asked to link.
type linkingForge struct {
	fakeNPMRegistryResolver

	refuse error
	linked []string
}

func (f *linkingForge) LinkForgePackage(_ context.Context, packageType, name string) error {
	f.linked = append(f.linked, packageType+" "+name)

	return f.refuse
}

// failingPublish is an npm whose registry has no tag yet and whose publish
// fails.
type failingPublish struct{ npmTagProbe }

func (p *failingPublish) RunInherit(ctx context.Context, dir string, stdout, stderr io.Writer, args ...string) error {
	if args[0] == "publish" {
		return fmt.Errorf("npm publish: %w", errs.ErrValidation)
	}

	return p.npmTagProbe.RunInherit(ctx, dir, stdout, stderr, args...)
}

// A package published to a forge that keeps packages under the owner is
// linked to the run's repository, also when an earlier run had published
// it, and never when the publication failed.
func TestNPMPublishLinksThePackageItPublished(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tarball := filepath.Join(dir, "pkg.tgz")
	writeNPMPackage(t, tarball, "@fixture/pkg", "1.2.3")

	body, err := os.ReadFile(tarball)
	require.NoError(t, err)

	digest := sha512.Sum512(body)
	same := `{"integrity":"sha512-` + base64.StdEncoding.EncodeToString(digest[:]) + `"}`

	for _, tc := range []struct {
		name       string
		ops        apppublish.NPMPublishOps
		wantError  error
		wantLinked []string
	}{
		{"published now", &npmTagProbe{response: "release-0.0.1: 0.0.1\n"}, nil, []string{"npm @fixture/pkg"}},
		{"published by an earlier run", &npmTagProbe{response: "release-1.2.3: 1.2.3\n", dist: same}, nil, []string{"npm @fixture/pkg"}},
		{"not published", &failingPublish{npmTagProbe{response: "release-0.0.1: 0.0.1\n"}}, errs.ErrValidation, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			forge := &linkingForge{reg: provider.ForgeNPMRegistry{Registry: "https://registry.example/", Token: "synthetic"}}

			err := apppublish.ForgePackagesNPMPublish(t.Context(), tc.ops, forge, io.Discard, io.Discard, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: "release-{version}"})
			require.ErrorIs(t, err, tc.wantError)
			require.Equal(t, tc.wantLinked, forge.linked)
		})
	}
}

// The package is published by the time it is linked: a link the forge
// refuses is a warning that names the package and the reason, and the
// publication stays a success.
func TestARefusedLinkIsAWarningAndThePublicationStands(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeNPMPackage(t, filepath.Join(dir, "pkg.tgz"), "@fixture/pkg", "1.2.3")

	forge := &linkingForge{
		reg:    provider.ForgeNPMRegistry{Registry: "https://registry.example/", Token: "synthetic"},
		refuse: errors.New("this Forgejo has no API that links a package to a repository"),
	}

	var said bytes.Buffer

	err := apppublish.ForgePackagesNPMPublish(t.Context(), &npmTagProbe{response: "release-0.0.1: 0.0.1\n"}, forge, io.Discard, &said, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: "release-{version}"})
	require.NoError(t, err)
	require.Equal(t, "Warning: npm package @fixture/pkg is published but not linked to the repository: this Forgejo has no API that links a package to a repository\n", said.String())
}

// Each Maven publication of an uploaded directory is one package, named by
// its coordinates. A forge without the role is asked nothing.
func TestMavenPublicationsAreLinkedByTheirCoordinates(t *testing.T) {
	t.Parallel()

	forge := &linkingForge{}
	apppublish.LinkMavenPublications(t.Context(), forge, io.Discard, publicationFixture(t), "1.2.3", false)
	require.Equal(t, []string{"maven org.example:demo"}, forge.linked)

	var said bytes.Buffer

	apppublish.LinkMavenPublications(t.Context(), fakeNPMRegistryResolver{}, &said, t.TempDir(), "1.2.3", false)
	require.Empty(t, said.String())
}
