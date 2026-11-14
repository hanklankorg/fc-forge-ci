// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

func TestPrereleaseDestinationsAreCheckedBeforePreparation(t *testing.T) {
	for _, tc := range []struct {
		name, tag, track string
		kind             projecttype.Type
		target           config.PublishTarget
		allowed          bool
	}{
		{"play production beta", "v1.0.0-beta.2", "production", projecttype.GradleAndroid, config.PublishGooglePlay, false},
		{"play testing beta", "v1.0.0-beta.2", "internal", projecttype.GradleAndroid, config.PublishGooglePlay, true},
		{"play default beta", "v1.0.0-beta.2", "", projecttype.GradleAndroid, config.PublishGooglePlay, true},
		{"play production stable", "v1.0.0", "production", projecttype.GradleAndroid, config.PublishGooglePlay, true},
		{"ios beta lacks version mapping", "v1.0.0-beta.2", "", projecttype.XcodeIOS, "", false},
		{"ios stable", "v1.0.0", "", projecttype.XcodeIOS, "", true},
		{"central immutable beta", "v1.0.0-beta.2", "", projecttype.Maven, config.PublishMavenCentral, true},
		{"central mutable snapshot", "v1.0.0-SNAPSHOT", "", projecttype.Maven, config.PublishMavenCentral, false},
		{"forge mutable snapshot", "v1.0.0-SNAPSHOT", "", projecttype.Maven, config.PublishForgePackages, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact := config.Artifact{Name: "app", ProjectType: tc.kind}
			if tc.target != "" {
				artifact.PublishTo = []config.PublishTarget{tc.target}
			}

			switch tc.kind {
			case projecttype.Maven:
				artifact.BuildType = config.BuildTypeLibrary
			case projecttype.GradleAndroid:
				artifact.GradleAndroid = &config.GradleAndroidConfig{PackageName: "example.app", GooglePlayTrack: tc.track}
			case projecttype.XcodeIOS:
				artifact.XcodeIOS = &config.XcodeIOSConfig{Scheme: "App"}
			default:
				// Other types need no type-specific configuration.
			}

			cfg := &config.Config{Artifacts: []config.Artifact{artifact}}
			if err := config.Derive(cfg); err != nil {
				t.Fatal(err)
			}

			_, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{ConfigPlan: pipeline.NewConfigPlan(cfg), RefName: tc.tag, ReleasePolicy: "stable-prerelease", ReleaseSBOMs: "none", RepositoryVisibility: "public"})
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v: %v", tc.allowed, err)
			}
		})
	}
}
