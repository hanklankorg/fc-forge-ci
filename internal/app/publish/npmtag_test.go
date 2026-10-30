// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"context"
	"encoding/json"
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

	return nil
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
		{"same binding", "release-1.2.3-rc.1", "release-1.2.3-rc.1: 1.2.3-rc.1\n", 2, nil},
		{"dotted wrong version", "release.9.0.0", "{}", 0, errs.ErrInvalidConfig},
		{"dotted conflict", "release.{version}", "release.1.2.3-rc.1: 0.1.0", 1, errs.ErrValidation},
		{"dotted literal conflict", "release.1.2.3-rc.1", "release.1.2.3-rc.1: 0.1.0", 1, errs.ErrValidation},
		{"dotted new binding", "release.{version}", "release.0.1.0: 0.1.0", 2, nil},
		{"dotted same binding", "release.1.2.3-rc.1", "release.1.2.3-rc.1: 1.2.3-rc.1", 2, nil},
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

func TestNPMPublishDoesNotMistakeRegistryFailureForAbsence(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"E503", "E401", "E403", "ECONNRESET", "E404", "No dist-tags found for other", "No dist-tags found for pkg", "npm ERR! No dist-tags found for other", "npm ERR! No dist-tags found for pkg"} {
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

			probe := &npmTagProbe{viewErr: errs.ErrDependencyUnavailable, viewStderr: stderr}
			resolver := fakeNPMRegistryResolver{reg: provider.ForgeNPMRegistry{Registry: "https://registry.example/", Token: "synthetic"}}

			err := apppublish.ForgePackagesNPMPublish(t.Context(), probe, resolver, io.Discard, io.Discard, apppublish.ForgePackagesNPMPublishInput{WorkingDir: dir, Tag: "release-{version}"})
			if code == "E404" || code == "No dist-tags found for pkg" || code == "npm ERR! No dist-tags found for pkg" {
				require.NoError(t, err)
				require.Len(t, probe.calls, 2)
			} else {
				require.ErrorIs(t, err, errs.ErrDependencyUnavailable)
				require.Len(t, probe.calls, 1)
			}
		})
	}
}
