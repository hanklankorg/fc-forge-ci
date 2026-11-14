// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config_test

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// A private-dependency declaration is refused at validation when it lacks
// what its ecosystem needs to authenticate: npm a secure registry to scope
// the token to, Maven the consumer's own settings file. Gradle needs neither,
// and nothing is required of an artifact that does not opt in.
func TestValidate_PrivateDependencies(t *testing.T) {
	t.Parallel()

	const registryError = "dependency-registry"

	cases := []struct {
		name       string
		artifact   config.Artifact
		wantSubstr string
	}{
		{name: "npm without the opt-in needs no registry", artifact: config.Artifact{ProjectType: projecttype.NPM, NPM: &config.NPMConfig{}}},
		{name: "npm opt-in off ignores an insecure registry", artifact: config.Artifact{ProjectType: projecttype.NPM, NPM: &config.NPMConfig{DependencyRegistry: "http://registry.example"}}},
		{name: "npm https registry", artifact: config.Artifact{ProjectType: projecttype.NPM, NPM: &config.NPMConfig{PrivateDependencies: true, DependencyRegistry: "https://npm.pkg.github.com"}}},
		{name: "npm loopback http registry", artifact: config.Artifact{ProjectType: projecttype.NPM, NPM: &config.NPMConfig{PrivateDependencies: true, DependencyRegistry: "http://127.0.0.1:4873"}}},
		{name: "npm registry missing", artifact: config.Artifact{ProjectType: projecttype.NPM, NPM: &config.NPMConfig{PrivateDependencies: true}}, wantSubstr: registryError},
		{name: "npm http registry off loopback", artifact: config.Artifact{ProjectType: projecttype.NPM, NPM: &config.NPMConfig{PrivateDependencies: true, DependencyRegistry: "http://registry.example"}}, wantSubstr: "requires HTTPS"},
		{name: "npm registry with credentials", artifact: config.Artifact{ProjectType: projecttype.NPM, NPM: &config.NPMConfig{PrivateDependencies: true, DependencyRegistry: "https://reader:secret@registry.example"}}, wantSubstr: registryError},
		{name: "maven without the opt-in needs no settings", artifact: config.Artifact{ProjectType: projecttype.Maven, Maven: &config.MavenConfig{}}},
		{name: "maven with settings", artifact: config.Artifact{ProjectType: projecttype.Maven, Maven: &config.MavenConfig{PrivateDependencies: true, SettingsPath: ".forge-ci/maven/settings.xml"}}},
		{name: "maven settings missing", artifact: config.Artifact{ProjectType: projecttype.Maven, Maven: &config.MavenConfig{PrivateDependencies: true}}, wantSubstr: "private Maven dependencies require config.settings-path"},
		{name: "gradle needs no further setting", artifact: config.Artifact{ProjectType: projecttype.Gradle, Gradle: &config.GradleConfig{PrivateDependencies: true}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.artifact.Name = "app"

			err := config.Validate(&config.Config{Artifacts: []config.Artifact{tc.artifact}})
			if tc.wantSubstr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				return
			}

			if !isValidationError(err) {
				t.Fatalf("err = %v, want ValidationError", err)
			}

			if !strings.Contains(err.Error(), `artifact "app": `) || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("err should name the artifact and mention %q: %v", tc.wantSubstr, err)
			}

			if strings.Contains(err.Error(), "secret") {
				t.Errorf("err repeats the refused registry's credentials: %v", err)
			}
		})
	}
}
