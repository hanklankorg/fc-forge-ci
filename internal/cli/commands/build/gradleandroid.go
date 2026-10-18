// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/gradle"
	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/secret"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

func gradleAndroidCmd() *cli.Command {
	return &cli.Command{
		Name:  "gradle-android",
		Usage: "android (gradle) build pipeline",
		Commands: []*cli.Command{
			gradleAndroidRunCmd(nil),
			gradleAndroidListArtifactsCmd(),
		},
	}
}

// gradleAndroidRunCmd runs the whole Android release build in one step (the
// Android sibling of `build gradle run`). The job keeps only checkout +
// artifact upload around it.
func gradleAndroidRunCmd(ops appbuild.AndroidGradleOps) *cli.Command {
	return &cli.Command{
		Name:  subCmdRun,
		Usage: "run the full Android release build (artifact-names, keystore, metadata, tasks, build, SBOM)",
		Description: `Runs the whole Android build sequence in one step: compose artifact names,
   make ./gradlew executable, decode the signing keystore (when --enable-signing),
   resolve metadata + tasks, write secrets.properties, run the gradle build, and
   generate the Build SBOM. Signing requires ANDROID_KEYSTORE_PASSWORD,
   ANDROID_KEY_ALIAS and ANDROID_KEY_PASSWORD; password bytes are preserved and
   whitespace-only aliases are refused. These values are passed in the Gradle
   child environment, not argv. The keystore is decoded into a private directory
   outside the project. Injected secrets.properties is create-only: an occupied
   destination is refused before effects. Without injection, existing properties
   are left alone. Run-owned signing files are cleaned after build and SBOM,
   including on failure; cleanup errors are returned. Replaced properties are
   preserved. Emits artifact names + version for downstream upload/publish jobs.

   Both ./gradlew invocations and metadata use --working-dir. Inherited JDK/SDK
   runtime settings remain available; this command is not a build sandbox.

EXAMPLE:
   cd app && forge-ci build gradle-android run --build-types release --include-aab --enable-signing`,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "library", Sources: cli.EnvVars("ANDROID_LIBRARY"), Usage: "build an Android library release AAR rather than APK/AAB application variants"},
			&cli.StringFlag{Name: flagWorkingDir, Value: ".", Sources: cli.EnvVars("WORKING_DIRECTORY"), Usage: "project directory for metadata, the Gradle build and Build SBOM"},
			&cli.BoolFlag{Name: "include-date-stamp", Value: true, Sources: cli.EnvVars("INCLUDE_DATE_STAMP"), Usage: "append a YYYYMMDD-HHMMSS stamp to the artifact name"},
			&cli.StringFlag{Name: "prefix", Sources: cli.EnvVars("ARTIFACT_NAME_PREFIX"), Usage: "optional prefix prepended to every artifact name"},
			&cli.StringFlag{Name: flagRepositoryName, Sources: cli.EnvVars("REPOSITORY_NAME"), Usage: "repository basename used in the default name"},
			&cli.StringFlag{Name: flagFlavor, Sources: cli.EnvVars("PRODUCT_FLAVOR"), Usage: "Android product flavor"},
			&cli.StringFlag{Name: "name-override", Sources: cli.EnvVars("ARTIFACT_NAME"), Usage: "explicit artifact-name override"},
			&cli.BoolFlag{Name: "enable-signing", Sources: cli.EnvVars("ENABLE_SIGNING"), Usage: "decode the keystore and sign the release build"},
			&cli.StringFlag{Name: "keystore-base64-file", Usage: "file with the base64-encoded release keystore (\"-\" stdin; defaults to $ANDROID_KEYSTORE_BASE64; required when --enable-signing)"},
			&cli.StringFlag{Name: "secrets-base64-file", Usage: "file with the base64-encoded secrets.properties (\"-\" stdin; defaults to $SECRETS_PROPERTIES_BASE64; optional)"},
			&cli.StringFlag{Name: "tasks-override", Sources: cli.EnvVars("GRADLE_TASKS_OVERRIDE"), Usage: "explicit task list override; bypasses the build-type heuristics"},
			&cli.StringFlag{Name: "build-types", Value: "debug,release", Sources: cli.EnvVars("BUILD_TYPES"), Usage: "comma-separated Android build types to assemble"},
			&cli.BoolFlag{Name: "include-aab", Value: true, Sources: cli.EnvVars("INCLUDE_AAB"), Usage: "also emit bundleRelease (produces an AAB)"},
			&cli.StringFlag{Name: flagBuildModule, Value: defaultBuildModule, Sources: cli.EnvVars("BUILD_MODULE"), Usage: "gradle module name (e.g. \"app\")"},
			&cli.BoolFlag{Name: flagSkipTests, Sources: cli.EnvVars("SKIP_TESTS"), Usage: usageSkipTestTask},
			&cli.BoolFlag{Name: flagBuildSBOM, Value: true, Sources: cli.EnvVars("ENABLE_BUILD_SBOM"), Usage: "generate the cyclonedx-gradle-plugin Build SBOM (default true)"},
			&cli.StringFlag{Name: flagSBOMToolVersion, Sources: cli.EnvVars("CYCLONEDX_GRADLE_VERSION"), Usage: "pinned cyclonedx-gradle-plugin version (required when --build-sbom)"},
			&cli.StringFlag{Name: flagTempDir, Sources: cienv.TempDir(), Usage: "existing scratch parent for a private signing directory, outside the project dir"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			buildOps := ops
			if buildOps == nil {
				buildOps = &gradle.Adapter{Env: safeexec.EnvWithout(buildEnvironment(), "ANDROID_KEYSTORE_BASE64", "ANDROID_KEYSTORE_PATH", "ANDROID_KEYSTORE_PASSWORD", "ANDROID_KEY_ALIAS", "ANDROID_KEY_PASSWORD", "SECRETS_PROPERTIES_BASE64")}
			}

			keystoreBase64, err := secret.Resolve(cmd.String("keystore-base64-file"), "ANDROID_KEYSTORE_BASE64")
			if err != nil {
				return err
			}

			secretsBase64, err := secret.Resolve(cmd.String("secrets-base64-file"), "SECRETS_PROPERTIES_BASE64")
			if err != nil {
				return err
			}

			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return appbuild.AndroidReleaseBuild(ctx, d.OutputSink, d.SummarySink, buildOps, deps.Annotator(cmd), os.Stderr, os.Stderr, appbuild.AndroidReleaseBuildInput{
					Library: cmd.Bool("library"),
					ReleaseBuildOptions: appbuild.ReleaseBuildOptions{
						Dir:             cmd.String(flagWorkingDir),
						ArtifactName:    cmd.String("name-override"),
						SkipTests:       cmd.Bool(flagSkipTests),
						EnableBuildSBOM: cmd.Bool(flagBuildSBOM),
					},
					TempDir:                 cmd.String(flagTempDir),
					IncludeDateStamp:        cmd.Bool("include-date-stamp"),
					ArtifactNamePrefix:      cmd.String("prefix"),
					RepoName:                cmd.String(flagRepositoryName),
					ProductFlavor:           cmd.String(flagFlavor),
					EnableSigning:           cmd.Bool("enable-signing"),
					KeystoreBase64:          keystoreBase64,
					KeystorePassword:        os.Getenv("ANDROID_KEYSTORE_PASSWORD"),
					KeyAlias:                os.Getenv("ANDROID_KEY_ALIAS"),
					KeyPassword:             os.Getenv("ANDROID_KEY_PASSWORD"),
					SecretsPropertiesBase64: secretsBase64,
					GradleTasksOverride:     cmd.String("tasks-override"),
					BuildTypes:              cmd.String("build-types"),
					IncludeAAB:              cmd.Bool("include-aab"),
					BuildModule:             cmd.String(flagBuildModule),
					SBOMToolVersion:         cmd.String(flagSBOMToolVersion),
				})
			})
		},
	}
}

func gradleAndroidListArtifactsCmd() *cli.Command {
	return &cli.Command{
		Name:  "list-artifacts",
		Usage: "list APK/AAB files under <build-module>/build/outputs",
		Description: `EXAMPLE:
   forge-ci build gradle-android list-artifacts --build-module app`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagBuildModule, Value: defaultBuildModule, Sources: cli.EnvVars("BUILD_MODULE"), Usage: "gradle module under which the APK/AAB outputs live"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return appbuild.AndroidListArtifacts(os.Stderr, appbuild.AndroidListArtifactsInput{
				BuildModule: cmd.String(flagBuildModule),
			})
		},
	}
}
