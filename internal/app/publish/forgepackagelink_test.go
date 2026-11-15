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
	"slices"
	"strings"
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
// publication stays a success. A package the publication made and the forge
// does not list is a warning too.
func TestARefusedLinkIsAWarningAndThePublicationStands(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeNPMPackage(t, filepath.Join(dir, "pkg.tgz"), "@fixture/pkg", "1.2.3")

	for _, refuse := range []error{
		errors.New("this Forgejo has no API that links a package to a repository"),
		fmt.Errorf("it is %w among the npm packages of fixture", provider.ErrForgePackageNotListed),
	} {
		t.Run(refuse.Error(), func(t *testing.T) {
			t.Parallel()

			forge := &linkingForge{
				reg:    provider.ForgeNPMRegistry{Registry: "https://registry.example/", Token: "synthetic"},
				refuse: refuse,
			}

			var said bytes.Buffer

			err := apppublish.ForgePackagesNPMPublish(t.Context(), &npmTagProbe{response: "release-0.0.1: 0.0.1\n"}, forge, io.Discard, &said, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: "release-{version}"})
			require.NoError(t, err)
			require.Equal(t, "Warning: npm package @fixture/pkg is published but not linked to the repository: "+refuse.Error()+"\n", said.String())
		})
	}
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

// namingForge is a Maven registry that keeps packages under their owner and
// answers each link by the package's name.
type namingForge struct {
	fakeRegistryResolver

	answers map[string]error
	linked  []string
}

func (f *namingForge) LinkForgePackage(_ context.Context, packageType, name string) error {
	f.linked = append(f.linked, packageType+" "+name)

	return f.answers[name]
}

// reactorMaven is a Maven that writes one effective POM where it is asked,
// records the settings that model ran with, and deploys with deployErr.
type reactorMaven struct {
	body      string
	deployErr error

	models   int
	settings string
	deployed bool
}

func (m *reactorMaven) RunInherit(_ context.Context, _, _ io.Writer, args ...string) error {
	if slices.Contains(args, "deploy") {
		m.deployed = true

		return m.deployErr
	}

	m.models++

	if i := slices.Index(args, "--settings"); i >= 0 && i+1 < len(args) {
		settings, err := os.ReadFile(args[i+1])
		if err != nil {
			return err
		}

		m.settings = string(settings)
	}

	for _, arg := range args {
		if output, found := strings.CutPrefix(arg, "-Doutput="); found {
			return os.WriteFile(output, []byte(m.body), 0o600)
		}
	}

	return fmt.Errorf("mvn %s: %w", strings.Join(args, " "), errs.ErrValidation)
}

// A forge deploy resolves the reactor once, before anything is published,
// with an empty settings file as the deploy replaces the user's. Every
// project of it is then linked by its coordinates. One the forge does not
// list was not deployed, which is Maven's decision and no warning; a link
// the forge refuses is, and so is a reactor the forge lists none of.
func TestForgeMavenDeployLinksTheReactorProjects(t *testing.T) {
	t.Parallel()

	const reactor = `<projects><project><groupId>org.example</groupId><artifactId>parent</artifactId><version>1.0.0</version></project>` +
		`<project><groupId>org.example</groupId><artifactId>app</artifactId><version>1.0.0</version></project></projects>`

	notListed := fmt.Errorf("it is %w among the maven packages of owner", provider.ErrForgePackageNotListed)
	both := []string{"maven org.example:parent", "maven org.example:app"}

	for _, tc := range []struct {
		name       string
		answers    map[string]error
		wantLinked []string
		wantSaid   string
	}{
		{"every project deployed", nil, both, ""},
		{"a project not deployed", map[string]error{"org.example:parent": notListed}, both, ""},
		{"no project listed", map[string]error{"org.example:parent": notListed, "org.example:app": notListed}, both,
			"Warning: the Maven packages are published but not linked to the repository: the forge lists none of org.example:parent, org.example:app\n"},
		{"a link refused", map[string]error{"org.example:app": errors.New("no API")}, both,
			"Warning: maven package org.example:app is published but not linked to the repository: no API\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			forge := &namingForge{answers: tc.answers}
			maven := &reactorMaven{body: reactor}

			var said bytes.Buffer

			require.NoError(t, apppublish.ForgeMavenDeploy(t.Context(), maven, maven, forge, io.Discard, &said, apppublish.ForgePackagesDeployInput{CLIOpts: []string{"-B"}}, false))
			require.Equal(t, 1, maven.models)
			require.Equal(t, "<settings/>\n", maven.settings)
			require.True(t, maven.deployed)
			require.Equal(t, tc.wantLinked, forge.linked)
			require.Equal(t, tc.wantSaid, said.String())
		})
	}
}

// What the deploy cannot resolve the same way as its model, or would not
// link, fails before anything is published; a deploy that fails links
// nothing; and a forge that links nothing needs no model of a release.
func TestForgeMavenDeployChecksBeforePublishing(t *testing.T) {
	t.Parallel()

	const release = `<project><groupId>org.example</groupId><artifactId>app</artifactId><version>1.0.0</version></project>`

	failed := errors.New("deploy failed")

	for _, tc := range []struct {
		name       string
		forge      provider.ForgeMavenRegistryResolver
		body       string
		opts       []string
		snapshot   bool
		deployErr  error
		wantErr    error
		wantModels int
		deploys    bool
	}{
		{"options the model cannot share", fakeRegistryResolver{}, release, []string{"-T", "4"}, false, nil, errs.ErrUsage, 0, false},
		{"a release in snapshot mode", fakeRegistryResolver{}, release, nil, true, nil, errs.ErrValidation, 1, false},
		{"an effective POM without coordinates", &namingForge{}, `<settings/>`, nil, false, nil, errs.ErrMalformedInput, 1, false},
		{"a failed deploy", &namingForge{}, release, nil, false, failed, failed, 1, true},
		{"a release to a forge that links nothing", fakeRegistryResolver{}, release, nil, false, nil, nil, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			maven := &reactorMaven{body: tc.body, deployErr: tc.deployErr}

			err := apppublish.ForgeMavenDeploy(t.Context(), maven, maven, tc.forge, io.Discard, io.Discard, apppublish.ForgePackagesDeployInput{CLIOpts: tc.opts}, tc.snapshot)
			require.ErrorIs(t, err, tc.wantErr)
			require.Equal(t, tc.wantModels, maven.models)
			require.Equal(t, tc.deploys, maven.deployed)

			if forge, ok := tc.forge.(*namingForge); ok {
				require.Empty(t, forge.linked)
			}
		})
	}
}
