// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

func filesGroup() *cli.Command {
	return &cli.Command{
		Name:  "files",
		Usage: "release file-set manifest helpers",
		Description: `Release asset paths are workspace-relative and include the dist directory
prefix. Run manifest/asset operations from the workspace root, for example with
--dist-dir dist and an asset path of dist/app.tar.gz.`,
		Commands: []*cli.Command{
			filesCompareCmd(),
			filesCollectCmd(),
			filesImportGoReleaserCmd(),
			filesDeclareCmd(),
			filesManifestCmd(),
			filesValidateCmd(),
			filesListCmd(),
			filesChecksumFileCmd(),
			filesValidateChecksumsCmd(),
		},
	}
}

func releaseFilesFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: flagDistDir, Value: defaultDistDir, Usage: "dist directory containing GoReleaser artifacts.json and release files; use a workspace-relative path for manifest/asset operations"},
		&cli.StringFlag{Name: flagManifest, Value: apprelease.DefaultReleaseFilesManifest, Sources: cli.EnvVars("RELEASE_FILES_MANIFEST"), Usage: "release file manifest path; a manifest named here must exist"},
	}
}

// releaseFilesInput reads the dist-dir/manifest pair. A manifest named by the
// flag or one of its sources has to exist; an unset or empty one is the
// default path, whose absence selects GoReleaser discovery.
func releaseFilesInput(cmd *cli.Command) apprelease.FilesInput {
	return apprelease.FilesInput{
		DistDir:          cmd.String(flagDistDir),
		ManifestFile:     cmd.String(flagManifest),
		ManifestExplicit: explicitValue(cmd, flagManifest) != "",
	}
}

// explicitValue returns the value the caller gave a string flag, on the
// command line or through one of its sources, and "" when the flag holds its
// default. A source set to the empty string names nothing.
func explicitValue(cmd *cli.Command, flag string) string {
	if !cmd.IsSet(flag) {
		return ""
	}

	return strings.TrimSpace(cmd.String(flag))
}

func filesCollectCmd() *cli.Command {
	flags := append(releaseFilesFlags(), &cli.StringFlag{Name: flagOutput, Value: cliio.StdSentinel, Usage: "output JSON file ('-' for stdout)"})

	return &cli.Command{
		Name:  "collect",
		Usage: "collect publishable release assets from GoReleaser metadata and signed sidecars",
		Flags: flags,
		Action: func(_ context.Context, cmd *cli.Command) error {
			assets, err := apprelease.CollectReleaseAssets(releaseFilesInput(cmd))
			if err != nil {
				return err
			}

			return writeJSON(cmd.String(flagOutput), assets)
		},
	}
}

// sbomBundleFlags name the SBOM bundle a hand-off's SBOMs are written into.
// The project defaults to the repository's name, as on GitHub, and the
// version to the release tag the release jobs export.
func sbomBundleFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: flagProjectName, Sources: cli.EnvVars("PROJECT_NAME"), Usage: "project in the SBOM bundle name <project>-<version>-sboms.tar.gz (default: the name of --repository)"},
		&cli.StringFlag{Name: flagRepository, Sources: cienv.Repository(), Usage: "\"owner/repo\" slug whose name is the default --project-name"},
		&cli.StringFlag{Name: flagVersion, Sources: cli.EnvVars("RELEASE_TAG"), Usage: "release version or tag in the SBOM bundle name; a v prefix is dropped; required when the release has an SBOM"},
	}
}

func sbomBundleNaming(cmd *cli.Command) apprelease.SBOMBundleNaming {
	project := cmd.String(flagProjectName)
	if repository := cmd.String(flagRepository); project == "" && repository != "" {
		project = domainrelease.ProjectNameFromRepo("", repository)
	}

	return apprelease.SBOMBundleNaming{ProjectName: project, Version: cmd.String(flagVersion)}
}

func filesImportGoReleaserCmd() *cli.Command {
	flags := append(releaseFilesFlags(),
		&cli.StringSliceFlag{Name: flagEvidenceFile, Usage: "additional dist file to sign and publish as evidence (repeatable)"},
	)
	flags = append(flags, sbomBundleFlags()...)

	return &cli.Command{
		Name:  "import-goreleaser",
		Usage: "import GoReleaser metadata into an unsigned release file manifest before signing",
		Description: `Writes the manifest from the publishable GoReleaser artifacts and the named
evidence files, in the layout of every forge-ci release, as "release files
declare" writes it. The SBOMs GoReleaser wrote are bundled into
<project>-<version>-sboms.tar.gz, which the release publishes in their place,
and checksums.sha256 is written over the archives, the packages and that
bundle.

GoReleaser's own checksums are refused: set checksum.disable: true in the
GoReleaser configuration. It also refuses an existing manifest, checksums
file or SBOM bundle, and a dist that already holds the provenance statement
or a <file>.bundle or <file>.sig of an imported file: the import is the
unsigned hand-off, as "release files validate --unsigned" checks it.`,
		Flags: flags,
		Action: func(_ context.Context, cmd *cli.Command) error {
			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{
				FilesInput:       releaseFilesInput(cmd),
				SBOMBundleNaming: sbomBundleNaming(cmd),
				EvidenceFiles:    cmd.StringSlice(flagEvidenceFile),
			})

			return err
		},
	}
}

func filesDeclareCmd() *cli.Command {
	// The shared pair describes a manifest that is read; this one is written.
	flags := append([]cli.Flag{
		&cli.StringFlag{Name: flagDistDir, Value: defaultDistDir, Usage: "workspace-relative dist directory containing the declared files"},
		&cli.StringFlag{Name: flagManifest, Value: apprelease.DefaultReleaseFilesManifest, Sources: cli.EnvVars("RELEASE_FILES_MANIFEST"), Usage: "release file manifest to write; it must not exist"},
		&cli.StringSliceFlag{Name: flagAsset, Usage: "dist file to publish, as a path or a glob pattern the command expands; directories a pattern matches are skipped (repeatable)"},
		&cli.StringSliceFlag{Name: "sbom", Usage: "dist SBOM file to publish in the SBOM bundle, as a path or a glob pattern (repeatable)"},
		&cli.StringSliceFlag{Name: flagEvidenceFile, Usage: "additional dist file to sign and publish as evidence (repeatable)"},
		&cli.StringFlag{Name: flagChecksumsFile, Usage: "checksums file to write; it must not exist (default: checksums.sha256 in --dist-dir)"},
	}, sbomBundleFlags()...)

	return &cli.Command{
		Name:  "declare",
		Usage: "write the checksums file and the unsigned release file manifest for the files a producer names",
		Description: `The hand-off of a build that has no GoReleaser metadata to import. Bundles
the --sbom files into <project>-<version>-sboms.tar.gz in the dist directory,
each under its file name: the release publishes that one bundle, also when it
has one SBOM, and not the SBOM files. Then writes the sha256sum-format
checksums file over every --asset file and the bundle, by name, and the
manifest: the assets, the bundle, the checksums file and the named evidence
files. A pattern that names no file is refused.

It refuses an existing manifest, checksums file or SBOM bundle, two files with one name,
and a dist that already holds the provenance statement or a <file>.bundle or
<file>.sig of a declared file: the result is the unsigned hand-off, as "release
files validate --unsigned" checks it.

EXAMPLE:
   forge-ci release files declare --asset 'dist/*/*' --sbom dist/app.cdx.json`,
		Flags: flags,
		Action: func(_ context.Context, cmd *cli.Command) error {
			manifest, err := apprelease.DeclareReleaseFiles(apprelease.DeclareReleaseFilesInput{
				FilesInput:       releaseFilesInput(cmd),
				SBOMBundleNaming: sbomBundleNaming(cmd),
				Assets:           cmd.StringSlice(flagAsset),
				SBOMs:            cmd.StringSlice("sbom"),
				EvidenceFiles:    cmd.StringSlice(flagEvidenceFile),
				ChecksumsFile:    cmd.String(flagChecksumsFile),
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(os.Stderr, "Declared %d release files in %s (checksums: %s)\n", len(manifest.Assets), cmd.String(flagManifest), manifest.Checksums[0].Path)

			return nil
		},
	}
}

func filesManifestCmd() *cli.Command {
	flags := append(releaseFilesFlags(),
		&cli.StringFlag{Name: flagOutput, Usage: "output manifest file (defaults to --manifest; '-' for stdout)"},
		&cli.StringFlag{Name: "assets-json", Usage: "release files collect JSON to reuse instead of re-collecting ('-' for stdin)"},
		&cli.BoolFlag{Name: "unsigned", Usage: "extend an existing unsigned manifest with evidence without rediscovering producer metadata"},
		&cli.StringSliceFlag{Name: flagEvidenceFile, Usage: "additional file to publish and sign as evidence; requires --unsigned (repeatable)"},
		&cli.StringFlag{Name: "release-images-ledger", Usage: "write the image SBOMs this ledger pins into the SBOM bundle; requires --unsigned"},
		&cli.StringSliceFlag{Name: "image-sbom", Usage: "image SBOM file a trusted publisher wrote, admitted against --release-images-ledger; the named files must be exactly the index SBOMs and platform inventories the ledger pins (repeatable)"},
		&cli.BoolFlag{Name: "image-sboms-from-ledger", Usage: "take the image SBOMs from the dist directory by the pins of --release-images-ledger instead of naming each; an entry without a pin, or a base image, has none to bundle; any other pinned SBOM outside the dist directory is refused"},
		// Named by the trusted step that completes the bundle, not read from
		// the job's environment as a producer's hand-off reads them.
		&cli.StringFlag{Name: flagProjectName, Usage: "with --release-images-ledger: project in the name of an SBOM bundle the hand-off does not have yet (default: the name of --repository)"},
		&cli.StringFlag{Name: flagRepository, Usage: "with --release-images-ledger: \"owner/repo\" slug whose name is the default --project-name"},
		&cli.StringFlag{Name: flagVersion, Usage: "with --release-images-ledger: release version or tag in the name of an SBOM bundle the hand-off does not have yet, and in the names of the files --image-sbom names; a v prefix is dropped"},
	)

	return &cli.Command{
		Name:  "manifest",
		Usage: "write or finalize the versioned release file manifest",
		Description: `With a manifest at --manifest, finalizes it after signing: the declared file
set is kept and the provenance statement, its bundle and the <file>.bundle and
<file>.sig of declared files are added. An evidence file without its bundle is
refused. Without a manifest at the default path, or with --assets-json, writes
a fresh manifest from GoReleaser discovery; a manifest named with --manifest
must exist, so a fresh write to another path uses --output.

With --unsigned, preserve the producer's existing file set and add explicit
--evidence-file paths before signing. Refuses existing signatures, provenance,
duplicate paths and reclassification; does not read GoReleaser metadata.

Every manifest refuses the pipeline's own files: the release plan
(release-plan.json, snapshot-release-plan.json) and the release image ledger
(release-images.json). Later jobs read them as job outputs or run artifacts.

With --release-images-ledger, also writes the image SBOMs the ledger pins into
the release's SBOM bundle <project>-<version>-sboms.tar.gz, beside the SBOMs
of the build, and rewrites the bundle's line in the checksums file: each index
SBOM and the platform inventories it names, every file held to the checksum
the ledger or the index SBOM records. No image SBOM is published as a file of
its own, and an --evidence-file named like one is refused. This is the one
point where the bundle changes after the build hand-off: it runs before the
provenance statement and the signatures exist, and a dist that holds either
is refused. A hand-off without an SBOM gets its bundle here.

--image-sbom names each file a trusted publisher wrote; they must be exactly
the ledger's set, named as the engine names a release image's SBOMs, with
the version of --version and one release commit. --image-sboms-from-ledger takes them from the dist directory
by the ledger's pins, for a ledger a consumer's jobs handed over: an entry
without a pin has its SBOM generated and attested by the signer and none to
bundle.`,
		Flags: flags,
		Action: func(_ context.Context, cmd *cli.Command) error {
			input := apprelease.WriteReleaseFileManifestInput{
				FilesInput:     releaseFilesInput(cmd),
				OutputFile:     cmd.String(flagOutput),
				AssetsJSONFile: cmd.String("assets-json"),
				Unsigned:       cmd.Bool("unsigned"),
				EvidenceFiles:  cmd.StringSlice(flagEvidenceFile),
				ReleaseImages: apprelease.ImageSBOMs{
					Ledger:     cmd.String("release-images-ledger"),
					Files:      cmd.StringSlice("image-sbom"),
					FromLedger: cmd.Bool("image-sboms-from-ledger"),
					ReleaseTag: cmd.String(flagVersion),
				},
				SBOMBundleNaming: sbomBundleNaming(cmd),
			}

			_, err := apprelease.WriteReleaseFileManifest(input)

			return err
		},
	}
}

func filesValidateCmd() *cli.Command {
	return &cli.Command{
		Name:  "validate",
		Usage: "validate the release file manifest",
		Description: `Checks the manifest's version, entries and classification against the dist
directory.

--unsigned is the check for a producer's hand-off, run before any signing
verb: it also refuses a manifest that declares provenance or a signature, and
a dist that already holds the provenance statement or a <file>.bundle or
<file>.sig of a declared file. Signing writes those files and "release files
manifest" publishes the ones it finds, so one that exists before signing would
be published as the signer's.

The release's artifacts.yml (--artifacts-config, by default
.forge-ci/artifacts.yml, which may be absent) names the artifacts whose release
build hands over a build SBOM: a Maven, npm, Gradle or Android artifact, or an
artifact-first Go or Cargo one, whose sboms keep the build layer. A manifest
that then declares no SBOM is refused. An Xcode, Python or meta artifact, a
docs or script project, writes none and needs none. A container-first Go or
Cargo artifact is built inside its image: forge-ci writes its build SBOM from
the lockfile (sbom assemble --artifacts-config), and the SBOM bundle must hold
it, named <artifact>-<version>[-<sha>]-build-sbom.cyclonedx.json.`,
		Flags: append(releaseFilesFlags(),
			&cli.BoolFlag{Name: "unsigned", Usage: "require the unsigned hand-off state: no declared provenance or signature, and no provenance statement or signature of a declared file in the dist"},
			&cli.StringFlag{Name: flagArtifactsConfig, Usage: "artifacts.yml of the release, whose artifacts with a build SBOM need one in the manifest (default: .forge-ci/artifacts.yml, which may be absent)"},
		),
		Action: func(_ context.Context, cmd *cli.Command) error {
			path := cmd.String(flagArtifactsConfig)
			if path == "" {
				path = defaultArtifactsConfig
			}

			plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{Path: path}, deps.Annotator(cmd), cmd.String(flagArtifactsConfig) == "")
			if err != nil {
				return err
			}

			if err := apprelease.ValidateReleaseFileManifest(apprelease.ValidateReleaseFileManifestInput{
				FilesInput:               releaseFilesInput(cmd),
				Unsigned:                 cmd.Bool("unsigned"),
				BuildSBOMArtifacts:       plans.BuildSBOMArtifacts,
				ContainerFirstBuildSBOMs: plannedNames(plans.ContainerFirstBuildSBOMs),
			}); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(os.Stderr, "Release file manifest validated: %s\n", cmd.String(flagManifest))

			return nil
		},
	}
}

func filesListCmd() *cli.Command {
	flags := append(releaseFilesFlags(), &cli.StringFlag{Name: "section", Value: "assets", Usage: "manifest section to list: assets, checksums, sboms, evidence, provenance"})

	return &cli.Command{
		Name:  "list",
		Usage: "print release file paths for a manifest section, falling back to discovery when no manifest exists",
		Flags: flags,
		Action: func(_ context.Context, cmd *cli.Command) error {
			paths, err := apprelease.ListReleaseFiles(apprelease.ListReleaseFilesInput{
				FilesInput: releaseFilesInput(cmd),
				Section:    cmd.String("section"),
			})
			if err != nil {
				return err
			}

			if len(paths) == 0 {
				return nil
			}

			_, _ = fmt.Fprintln(os.Stdout, strings.Join(paths, "\n"))

			return nil
		},
	}
}

func filesChecksumFileCmd() *cli.Command {
	return &cli.Command{
		Name:  "checksum-file",
		Usage: "print the single release checksums file path",
		Description: `A standalone scripting helper: resolve the one checksums file through the
release-file manifest without guessing its filename. It prints the path, not a
digest, and does not write or sign anything. The delivery layers call higher-level
verbs, but scripts can use this helper before passing the file to another tool.

EXAMPLE:
   forge-ci release files checksum-file --dist-dir dist`,
		Flags: releaseFilesFlags(),
		Action: func(_ context.Context, cmd *cli.Command) error {
			path, err := apprelease.FindReleaseChecksumFile(releaseFilesInput(cmd))
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintln(os.Stdout, path)

			return nil
		},
	}
}

func filesValidateChecksumsCmd() *cli.Command {
	flags := append(releaseFilesFlags(), &cli.StringFlag{Name: flagChecksumsFile, Sources: cli.EnvVars("CHECKSUMS_FILE"), Usage: "checksums file to validate (defaults to release files checksum-file)"})

	return &cli.Command{
		Name:  "validate-checksums",
		Usage: "validate that the checksums file names and matches every public release asset exactly once",
		Flags: flags,
		Action: func(_ context.Context, cmd *cli.Command) error {
			return apprelease.ValidateReleaseChecksums(apprelease.ValidateReleaseChecksumsInput{
				FilesInput:    releaseFilesInput(cmd),
				ChecksumsFile: cmd.String(flagChecksumsFile),
			})
		},
	}
}

func writeJSON(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}

	body = append(body, '\n')

	if path != cliio.StdSentinel {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // public release metadata under workspace.
			return fmt.Errorf("create output directory: %w", err)
		}
	}

	return cliio.WriteFile(path, body, 0o644)
}

func plannedNames(artifacts []pipeline.PlannedArtifact) []string {
	names := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		names = append(names, artifact.Name)
	}

	return names
}
