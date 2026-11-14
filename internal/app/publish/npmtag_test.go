// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

type npmTagProbe struct {
	response   string
	dist       string
	viewErr    error
	viewStderr string
	calls      [][]string
	credential string
}

// Only queries reach the inert registry; publication is recorded by the fixture.
type localNPMTagProbe struct {
	npmTagProbe

	env []string
}

func (p *localNPMTagProbe) RunInherit(ctx context.Context, dir string, stdout, stderr io.Writer, args ...string) error {
	if args[0] == "publish" {
		return p.npmTagProbe.RunInherit(ctx, dir, stdout, stderr, args...)
	}

	p.calls = append(p.calls, args)
	cmd := exec.CommandContext(ctx, "npm", args...)

	var diagnostic evidence.Diagnostic

	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, p.env, stdout, io.MultiWriter(stderr, &diagnostic)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm query: %w; stderr: %s", err, diagnostic.Text(p.env, "synthetic"))
	}

	return nil
}

func TestNPMPublishNativeQueryWithoutLatest(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm is not installed")
	}

	for _, tc := range []struct {
		name     string
		versions []string
		conflict bool
	}{
		{"empty tag map", nil, false},
		{"single version", []string{"0.1.0"}, false},
		{"multiple versions", []string{"0.1.0", "1.0.0"}, false},
		{"conflicting desired tag", []string{"0.1.0", "1.0.0"}, true},
		{"prerelease only", []string{"0.1.0-rc.1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags := map[string]string{}

			versions := map[string]any{}
			for _, version := range tc.versions {
				tags["release."+version] = version
				versions[version] = map[string]string{"name": "@fixture/pkg", "version": version}
			}

			if tc.conflict {
				tags["release.1.2.3"] = "0.1.0"
			}

			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
				response.Header().Set("Content-Type", "application/json")

				switch r.URL.Path {
				case "/@fixture/pkg":
					_ = json.NewEncoder(response).Encode(map[string]any{"name": "@fixture/pkg", "dist-tags": tags, "versions": versions})
				case "/-/package/@fixture/pkg/dist-tags":
					_ = json.NewEncoder(response).Encode(tags)
				default:
					http.Error(response, "unexpected query", http.StatusBadRequest)
				}
			}))
			defer server.Close()

			dir := t.TempDir()
			config := filepath.Join(dir, "npmrc")
			require.NoError(t, os.WriteFile(config, nil, 0o600))
			env := append(os.Environ(), "npm_config_cache="+filepath.Join(dir, "cache"), "npm_config_userconfig="+config, "npm_config_update_notifier=false")
			// Reproduce the original defect against the same packument: success,
			// but no JSON because the registry has no latest binding.
			cmd := exec.CommandContext(t.Context(), "npm", "view", "@fixture/pkg", "dist-tags", "--json", "--registry", server.URL)
			cmd.Dir, cmd.Env = dir, env

			var diagnostic evidence.Diagnostic

			cmd.Stderr = &diagnostic
			output, err := cmd.Output()
			require.NoError(t, err, "npm query stderr: %s", diagnostic.Text(env))
			require.Empty(t, strings.TrimSpace(string(output)))

			writeNPMPackage(t, filepath.Join(dir, "pkg.tgz"), "@fixture/pkg", "1.2.3")

			probe := &localNPMTagProbe{env: env}
			resolver := fakeNPMRegistryResolver{reg: provider.ForgeNPMRegistry{Registry: server.URL + "/", Token: "synthetic"}}

			err = apppublish.ForgePackagesNPMPublish(t.Context(), probe, resolver, io.Discard, io.Discard, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: "release.{version}"})
			if tc.conflict {
				require.ErrorIs(t, err, errs.ErrValidation)
				require.Len(t, probe.calls, 1)
			} else {
				require.NoError(t, err)
				require.Len(t, probe.calls, 2)
			}
		})
	}
}

func (p *npmTagProbe) RunInherit(_ context.Context, _ string, stdout, stderr io.Writer, args ...string) error {
	p.calls = append(p.calls, args)
	for i, arg := range args {
		if arg == "--userconfig" {
			p.credential = args[i+1]
		}
	}

	if args[0] == "dist-tag" {
		if _, err := io.WriteString(stdout, p.response); err != nil {
			return err
		}

		_, _ = io.WriteString(stderr, p.viewStderr)

		return p.viewErr
	}

	if args[0] == "view" {
		_, err := io.WriteString(stdout, p.dist)

		return err
	}

	return nil
}

// A retry of a release that published some of its packages finds them there:
// the version the release's tag already names is this release's only with
// this tarball's content, and is then not published again.
func TestNPMPublishSkipsOnlyTheSameContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tarball := filepath.Join(dir, "pkg.tgz")
	writeNPMPackage(t, tarball, "pkg", "1.2.3")

	body, err := os.ReadFile(tarball)
	require.NoError(t, err)

	strong, weak := sha512.Sum512(body), sha1.Sum(body)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(strong[:])
	shasum := hex.EncodeToString(weak[:])

	for _, tc := range []struct {
		name, dist string
		wantError  error
	}{
		{"same integrity", `{"integrity":"` + integrity + `","shasum":"other"}`, nil},
		{"same shasum alone", `{"shasum":"` + shasum + `"}`, nil},
		{"other integrity", `{"integrity":"sha512-b3RoZXI=","shasum":"` + shasum + `"}`, errs.ErrValidation},
		{"other shasum", `{"shasum":"da39a3ee5e6b4b0d3255bfef95601890afd80709"}`, errs.ErrValidation},
		{"weaker integrity", `{"integrity":"sha1-b3RoZXI=","shasum":"` + shasum + `"}`, errs.ErrMalformedInput},
		{"no digest", `{}`, errs.ErrMalformedInput},
		{"no version", ``, errs.ErrMalformedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			probe := &npmTagProbe{response: "release-1.2.3: 1.2.3\n", dist: tc.dist}
			resolver := fakeNPMRegistryResolver{reg: provider.ForgeNPMRegistry{Registry: "https://registry.example/", Token: "synthetic"}}

			err := apppublish.ForgePackagesNPMPublish(t.Context(), probe, resolver, io.Discard, io.Discard, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: "release-{version}"})
			require.ErrorIs(t, err, tc.wantError)
			require.Len(t, probe.calls, 2, "the tags and the version are read; nothing is published")
			require.Equal(t, []string{"view", "pkg@1.2.3", "dist", "--json", "--userconfig", probe.credential, "--registry", "https://registry.example/"}, probe.calls[1])
		})
	}
}

// The tarballs a release publishes are its planned projects' own: each packs
// the name and version of its project's package.json, and dist holds no other.
func TestNPMTarballsAreThePlannedProjects(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		packed    map[string][2]string
		wantError string
	}{
		{"each project's", map[string][2]string{"a.tgz": {"@fixture/ui", "1.0.0"}, "b.tgz": {"lib", "2.0.0"}}, ""},
		{"another version", map[string][2]string{"a.tgz": {"@fixture/ui", "1.0.1"}, "b.tgz": {"lib", "2.0.0"}}, "packs @fixture/ui@1.0.0"},
		{"another name", map[string][2]string{"a.tgz": {"@fixture/other", "1.0.0"}, "b.tgz": {"lib", "2.0.0"}}, "packs @fixture/ui@1.0.0"},
		{"a missing package", map[string][2]string{"b.tgz": {"lib", "2.0.0"}}, "packs @fixture/ui@1.0.0"},
		{"an unplanned package", map[string][2]string{"a.tgz": {"@fixture/ui", "1.0.0"}, "b.tgz": {"lib", "2.0.0"}, "c.tgz": {"extra", "1.0.0"}}, "no npm project the plan publishes"},
		{"one package twice", map[string][2]string{"a.tgz": {"@fixture/ui", "1.0.0"}, "b.tgz": {"lib", "2.0.0"}, "c.tgz": {"lib", "2.0.0"}}, "both pack lib@2.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			source := t.TempDir()
			dist := filepath.Join(source, "dist")
			require.NoError(t, os.MkdirAll(filepath.Join(source, "packages", "ui"), 0o700))
			require.NoError(t, os.Mkdir(dist, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(source, "packages", "ui", "package.json"), []byte(`{"name":"@fixture/ui","version":"1.0.0"}`), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"name":"lib","version":"2.0.0"}`), 0o600))

			for file, identity := range tc.packed {
				writeNPMPackage(t, filepath.Join(dist, file), identity[0], identity[1])
			}

			names, err := apppublish.NPMTarballs(source, dist, []string{".", "packages/ui"})
			if tc.wantError != "" {
				require.ErrorIs(t, err, errs.ErrValidation)
				require.ErrorContains(t, err, tc.wantError)

				return
			}

			require.NoError(t, err)
			require.Equal(t, []string{"b.tgz", "a.tgz"}, names, "in the order of the projects")
		})
	}

	_, err := apppublish.NPMTarballs(t.TempDir(), t.TempDir(), []string{"../outside"})
	require.Error(t, err, "a project directory outside the checkout")
}

func TestNPMPublishEnvironmentDropsWhatConfiguresNPM(t *testing.T) {
	t.Parallel()

	// The route and trust of a proxied or private-CA runner stay.
	kept := []string{"PATH=/usr/bin", "HOME=/home/job", "NODE_ENV=production", "HTTPS_PROXY=http://proxy.example:3128", "NODE_EXTRA_CA_CERTS=/etc/ca.pem"}
	require.Equal(t, kept, apppublish.NPMPublishEnvironment([]string{
		"PATH=/usr/bin", "npm_config_registry=https://other.example/", "NPM_CONFIG_USERCONFIG=/builds/.npmrc",
		"Npm_Config_Globalconfig=/x", "HOME=/home/job", "NODE_OPTIONS=--require /builds/hook.js", "NODE_ENV=production",
		"NODE_PATH=/builds/modules", "NODE_TLS_REJECT_UNAUTHORIZED=0", "HTTPS_PROXY=http://proxy.example:3128", "NODE_EXTRA_CA_CERTS=/etc/ca.pem",
	}))
}

// A package publishes to the forge's registry alone: one its publishConfig
// names, for every package or for a scope, is that registry or the package
// is refused before npm is asked anything.
func TestNPMPublishRefusesAnotherRegistryInThePackage(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		config  map[string]string
		refused bool
	}{
		"the forge's registry":       {map[string]string{"registry": "https://registry.example"}, false},
		"the forge's for its scope":  {map[string]string{"@fixture:registry": "https://registry.example/"}, false},
		"another registry":           {map[string]string{"registry": "https://other.example/"}, true},
		"another for its scope":      {map[string]string{"@fixture:registry": "https://other.example/"}, true},
		"another for a spelt scope":  {map[string]string{"@Fixture:Registry": "https://other.example/"}, true},
		"another beside the forge's": {map[string]string{"registry": "https://registry.example/", "@fixture:registry": "https://other.example/"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeNPMPackage(t, filepath.Join(dir, "pkg-0.1.0.tgz"), "@fixture/pkg", "0.1.0", tc.config)

			probe := &npmTagProbe{response: "release-0.0.1: 0.0.1\n"}
			resolver := fakeNPMRegistryResolver{reg: provider.ForgeNPMRegistry{Registry: "https://registry.example/", Token: "synthetic"}}
			err := apppublish.ForgePackagesNPMPublish(t.Context(), probe, resolver, io.Discard, io.Discard, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: "release-{version}"})

			if !tc.refused {
				require.NoError(t, err)
				require.Len(t, probe.calls, 2)

				return
			}

			require.ErrorIs(t, err, errs.ErrInvalidConfig)
			require.ErrorContains(t, err, "conflicts with the selected registry")
			require.Empty(t, probe.calls)
		})
	}
}

func TestNPMPublishChecksTagBeforeMutation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, setting, response string
		wantCalls               int
		wantError               error
	}{
		{"missing tag", "", "{}", 0, errs.ErrInvalidConfig},
		{"latest", "latest", "{}", 0, errs.ErrInvalidConfig},
		{"next", "next", "{}", 0, errs.ErrInvalidConfig},
		{"wrong version", "release-9.0.0", "{}", 0, errs.ErrInvalidConfig},
		{"conflict", "release-{version}", "release-1.2.3-rc.1: 0.1.0", 1, errs.ErrValidation},
		{"invalid response", "release-{version}", `not JSON`, 1, errs.ErrMalformedInput},
		{"new binding", "release-{version}", "release-0.1.0: 0.1.0\n", 2, nil},
		{"dotted wrong version", "release.9.0.0", "{}", 0, errs.ErrInvalidConfig},
		{"dotted conflict", "release.{version}", "release.1.2.3-rc.1: 0.1.0", 1, errs.ErrValidation},
		{"dotted literal conflict", "release.1.2.3-rc.1", "release.1.2.3-rc.1: 0.1.0", 1, errs.ErrValidation},
		{"dotted new binding", "release.{version}", "release.0.1.0: 0.1.0", 2, nil},
		{"multiple versions without latest", "release.{version}", "release.0.1.0: 0.1.0\nrelease.1.0.0: 1.0.0\n", 2, nil},
		{"multiple versions conflicting without latest", "release.{version}", "release.0.1.0: 0.1.0\nrelease.1.2.3-rc.1: 1.0.0\n", 1, errs.ErrValidation},
		{"empty output", "release.{version}", "", 1, errs.ErrMalformedInput},
		{"invalid version", "release.{version}", "release.1.0.0: garbage", 1, errs.ErrMalformedInput},
		{"duplicate tag", "release.{version}", "release.1.0.0: 1.0.0\nrelease.1.0.0: 2.0.0", 1, errs.ErrMalformedInput},
		{"invalid tag", "release.{version}", "bad tag: 1.0.0", 1, errs.ErrMalformedInput},
		{"oversized output", "release.{version}", strings.Repeat("x", (1<<20)+1), 1, errs.ErrMalformedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeNPMPackage(t, filepath.Join(dir, "misleading-9.0.0.tgz"), "@fixture/pkg", "1.2.3-rc.1")

			probe := &npmTagProbe{response: tc.response}
			resolver := fakeNPMRegistryResolver{reg: provider.ForgeNPMRegistry{Registry: "https://registry.example/", Token: "synthetic"}}
			err := apppublish.ForgePackagesNPMPublish(t.Context(), probe, resolver, io.Discard, io.Discard, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: tc.setting})
			require.ErrorIs(t, err, tc.wantError)
			require.Len(t, probe.calls, tc.wantCalls)

			if tc.wantCalls > 0 {
				require.Equal(t, []string{"dist-tag", "ls", "@fixture/pkg", "--userconfig", probe.credential, "--registry", "https://registry.example/"}, probe.calls[0])
			}

			if tc.wantCalls == 2 {
				wantTag := strings.ReplaceAll(tc.setting, "{version}", "1.2.3-rc.1")
				require.Equal(t, []string{"publish", "./misleading-9.0.0.tgz", "--userconfig", probe.credential, "--registry", "https://registry.example/", "--tag", wantTag, "--ignore-scripts"}, probe.calls[1])
			}

			if probe.credential != "" {
				_, err := os.Stat(probe.credential)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

// A failed read of the registry is not an absent package, and it is not
// silent: npm's own error output reaches the job's log, without the
// credential, and its error code decides the class. Only a registry that
// could not be reached is unavailable; a status and a certificate npm
// refused stay npm's failure.
func TestNPMPublishDoesNotMistakeRegistryFailureForAbsence(t *testing.T) {
	t.Parallel()

	for code, want := range map[string]error{
		"E503": errs.ErrValidation, "E401": errs.ErrValidation, "E403": errs.ErrValidation,
		"SELF_SIGNED_CERT_IN_CHAIN": errs.ErrValidation, "UNABLE_TO_GET_ISSUER_CERT_LOCALLY": errs.ErrValidation,
		"ECONNRESET": errs.ErrDependencyUnavailable, "ENOTFOUND": errs.ErrDependencyUnavailable, "ETIMEDOUT": errs.ErrDependencyUnavailable,
		"E429": errs.ErrRateLimited,
		"E404": nil, "No dist-tags found for pkg": nil, "npm ERR! No dist-tags found for pkg": nil,
		"No dist-tags found for other": errs.ErrValidation, "npm ERR! No dist-tags found for other": errs.ErrValidation,
	} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeNPMPackage(t, filepath.Join(dir, "pkg.tgz"), "pkg", "1.2.3")

			stderr := "npm error code " + code
			if strings.HasPrefix(code, "No dist-tags") {
				stderr = "npm error " + code
			}

			if strings.HasPrefix(code, "npm ERR!") {
				stderr = code
			}

			stderr += "\nnpm error request to https://registry.example/pkg failed with synthetic\n"

			// What the npm adapter returns for a non-zero exit.
			probe := &npmTagProbe{viewErr: fmt.Errorf("npm dist-tag: exit status 1: %w", errs.ErrValidation), viewStderr: stderr}
			resolver := fakeNPMRegistryResolver{reg: provider.ForgeNPMRegistry{Registry: "https://registry.example/", Token: "synthetic"}}

			var said bytes.Buffer

			err := apppublish.ForgePackagesNPMPublish(t.Context(), probe, resolver, io.Discard, &said, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: "release-{version}"})
			if want == nil {
				require.NoError(t, err)
				require.Len(t, probe.calls, 2)

				return
			}

			require.ErrorIs(t, err, want)
			require.Len(t, probe.calls, 1)
			require.Equal(t, strings.ReplaceAll(stderr, "synthetic", "***"), said.String(), "npm's error output, without the credential")

			if !errors.Is(want, errs.ErrValidation) {
				require.NotErrorIs(t, err, errs.ErrValidation, "the exit code is the class's")
				require.NotContains(t, err.Error(), "validation failed")
			}
		})
	}
}
