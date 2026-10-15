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

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/cliio"
)

func filesGroup() *cli.Command {
	return &cli.Command{
		Name:  "files",
		Usage: "release file-set manifest helpers",
		Description: `Release asset paths are workspace-relative and include the dist directory
prefix. Run manifest/asset operations from the workspace root, for example with
--dist-dir dist and an asset path of dist/app.tar.gz.`,
		Commands: []*cli.Command{
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

func filesImportGoReleaserCmd() *cli.Command {
	flags := append(releaseFilesFlags(),
		&cli.StringSliceFlag{Name: "evidence-file", Usage: "additional dist file to sign and publish as evidence (repeatable)"},
	)

	return &cli.Command{
		Name:  "import-goreleaser",
		Usage: "import GoReleaser metadata into an unsigned release file manifest before signing",
		Description: `Writes the manifest from the publishable GoReleaser artifacts and the named
evidence files. It refuses an existing manifest, and a dist that already holds
the provenance statement or a <file>.bundle or <file>.sig of an imported file:
the import is the unsigned hand-off, as "release files validate --unsigned"
checks it.`,
		Flags: flags,
		Action: func(_ context.Context, cmd *cli.Command) error {
			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{
				FilesInput:    releaseFilesInput(cmd),
				EvidenceFiles: cmd.StringSlice("evidence-file"),
			})

			return err
		},
	}
}

func filesDeclareCmd() *cli.Command {
	// The shared pair describes a manifest that is read; this one is written.
	flags := []cli.Flag{
		&cli.StringFlag{Name: flagDistDir, Value: defaultDistDir, Usage: "workspace-relative dist directory containing the declared files"},
		&cli.StringFlag{Name: flagManifest, Value: apprelease.DefaultReleaseFilesManifest, Sources: cli.EnvVars("RELEASE_FILES_MANIFEST"), Usage: "release file manifest to write; it must not exist"},
		&cli.StringSliceFlag{Name: "asset", Usage: "dist file to publish, as a path or a glob pattern the command expands; directories a pattern matches are skipped (repeatable)"},
		&cli.StringSliceFlag{Name: "sbom", Usage: "dist file to publish and classify as an SBOM, as a path or a glob pattern (repeatable)"},
		&cli.StringSliceFlag{Name: "evidence-file", Usage: "additional dist file to sign and publish as evidence (repeatable)"},
		&cli.StringFlag{Name: flagChecksumsFile, Usage: "checksums file to write; it must not exist (default: checksums.txt in --dist-dir)"},
	}

	return &cli.Command{
		Name:  "declare",
		Usage: "write the checksums file and the unsigned release file manifest for the files a producer names",
		Description: `The hand-off of a build that has no GoReleaser metadata to import. Writes the
sha256sum-format checksums file over every --asset and --sbom file, by name,
then the manifest: the assets, the SBOMs, the checksums file and the named
evidence files. A pattern that names no file is refused.

It refuses an existing manifest or checksums file, two files with one name,
and a dist that already holds the provenance statement or a <file>.bundle or
<file>.sig of a declared file: the result is the unsigned hand-off, as "release
files validate --unsigned" checks it.

EXAMPLE:
   forge-ci release files declare --asset 'dist/*/*' --sbom dist/app.cdx.json`,
		Flags: flags,
		Action: func(_ context.Context, cmd *cli.Command) error {
			manifest, err := apprelease.DeclareReleaseFiles(apprelease.DeclareReleaseFilesInput{
				FilesInput:    releaseFilesInput(cmd),
				Assets:        cmd.StringSlice("asset"),
				SBOMs:         cmd.StringSlice("sbom"),
				EvidenceFiles: cmd.StringSlice("evidence-file"),
				ChecksumsFile: cmd.String(flagChecksumsFile),
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
	)

	return &cli.Command{
		Name:  "manifest",
		Usage: "write or finalize the versioned release file manifest",
		Description: `With a manifest at --manifest, finalizes it after signing: the declared file
set is kept and the provenance statement, its bundle and the <file>.bundle and
<file>.sig of declared files are added. An evidence file without its bundle is
refused. Without a manifest at the default path, or with --assets-json, writes
a fresh manifest from GoReleaser discovery; a manifest named with --manifest
must exist, so a fresh write to another path uses --output.`,
		Flags: flags,
		Action: func(_ context.Context, cmd *cli.Command) error {
			input := apprelease.WriteReleaseFileManifestInput{
				FilesInput:     releaseFilesInput(cmd),
				OutputFile:     cmd.String(flagOutput),
				AssetsJSONFile: cmd.String("assets-json"),
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
be published as the signer's.`,
		Flags: append(releaseFilesFlags(),
			&cli.BoolFlag{Name: "unsigned", Usage: "require the unsigned hand-off state: no declared provenance or signature, and no provenance statement or signature of a declared file in the dist"},
		),
		Action: func(_ context.Context, cmd *cli.Command) error {
			if err := apprelease.ValidateReleaseFileManifest(apprelease.ValidateReleaseFileManifestInput{
				FilesInput: releaseFilesInput(cmd),
				Unsigned:   cmd.Bool("unsigned"),
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
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec,mnd // public release metadata under workspace.
			return fmt.Errorf("create output directory: %w", err)
		}
	}

	return cliio.WriteFile(path, body, 0o644) //nolint:gosec // public release metadata.
}
