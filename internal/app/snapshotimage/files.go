// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

var (
	errFilesOverlapBuild   = errors.New("snapshot file output must not overlap the build's own output")
	errNoBuiltFiles        = errors.New("built no file a snapshot keeps")
	errNoJVMFiles          = errors.New("built no jar or war in the build directory of the project or of a module; a project of pom packaging alone has no file to snapshot, set publish-files to false for it")
	errFileNamedAsManifest = errors.New("snapshot file output cannot contain a file named as its receipt")
)

// BuildFilesInput selects one planned file output, the directory its bundle
// is written below and the parent environment.
type BuildFilesInput struct {
	Name, OutputDir string
	Environment     []string
}

// BuildFiles builds one planned file output at the pinned source, with the
// build CLI and the credential-free environment an image's producer gets, and
// writes its bundle: the built files and the receipt that binds them to the
// source commit. The bundle is unsigned; the caller keeps it as a run
// artifact and publishes it nowhere else.
func BuildFiles(ctx context.Context, ops appversion.GenerateSnapshotOps, runner ArtifactBuildRunner, plan snapshotimage.Plan, in BuildFilesInput) error {
	if err := CheckSource(ctx, ops, plan.SourceSHA); err != nil {
		return err
	}

	planned, err := plan.File(in.Name)
	if err != nil {
		return err
	}

	output := filepath.Join(cmp.Or(in.OutputDir, snapshotimage.DefaultFilesDir), planned.Name)
	if err := newOutput(output, plan); err != nil {
		return err
	}

	// An npm build packs its tarball into the working directory itself, of
	// which only the top-level tarball is read; every other build has an
	// output directory of its own, which the bundle must stay out of.
	source := producerSource(planned)
	if planned.ProjectType != projecttype.NPM && (inside(source, output) || inside(output, source)) {
		return errFilesOverlapBuild
	}

	if err := runArtifactBuild(ctx, ops, runner, plan, planned, in.Environment); err != nil {
		return err
	}

	return writeFilesBundle(plan, planned, output)
}

// writeFilesBundle stages the artifact's built files with their receipt and
// installs the bundle as a new directory.
func writeFilesBundle(plan snapshotimage.Plan, planned pipeline.PlannedArtifact, output string) error {
	built, err := builtFiles(planned)
	if err != nil {
		return fmt.Errorf("snapshot file output %s: %w", planned.Name, err)
	}

	tmp, err := os.MkdirTemp("", "forge-ci-snapshot-files-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(tmp) }()

	root, err := pathsafe.OpenRoot(tmp)
	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	files := make([]snapshotimage.DependencyFile, 0, len(built))

	for _, kept := range built {
		if kept.file.Path == snapshotimage.FilesManifest {
			return fmt.Errorf("%w: %s", errFileNamedAsManifest, planned.Name)
		}

		if err := stageContextFile(kept.source, root, kept.file); err != nil {
			return err
		}

		files = append(files, kept.file)
	}

	receipt := snapshotimage.FilesReceipt{
		Version: snapshotimage.Version, SourceSHA: plan.SourceSHA, SourceBranch: plan.SourceBranch,
		SnapshotVersion: plan.ArtifactVersion(), Name: planned.Name, ProjectType: planned.ProjectType,
		Signing: snapshotimage.FilesSigning, Files: files,
	}
	if err := WriteJSON(filepath.Join(tmp, snapshotimage.FilesManifest), receipt); err != nil {
		return err
	}

	return exportTree(tmp, output, plan)
}

// builtFile is one kept file: where the build left it and what the bundle
// records of it, its path there included.
type builtFile struct {
	source string
	file   snapshotimage.DependencyFile
}

// builtFiles hashes the files a snapshot keeps of one build, sorted by their
// path in the bundle: every file below dist for Go and Cargo, the jars of
// every module for Maven and Gradle, and the one packed tarball for npm.
func builtFiles(planned pipeline.PlannedArtifact) ([]builtFile, error) {
	var (
		files []builtFile
		err   error
	)

	switch planned.ProjectType {
	case projecttype.NPM:
		files, err = npmFile(planned.WorkingDirectory)
	case projecttype.Maven, projecttype.Gradle:
		files, err = jvmFiles(planned)
	default:
		files, err = distFiles(producerSource(planned))
	}

	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		if planned.ProjectType == projecttype.Maven || planned.ProjectType == projecttype.Gradle {
			return nil, errNoJVMFiles
		}

		return nil, errNoBuiltFiles
	}

	slices.SortFunc(files, func(left, right builtFile) int { return strings.Compare(left.file.Path, right.file.Path) })

	return files, nil
}

func npmFile(source string) ([]builtFile, error) {
	tarballs, err := npmTarballs(source)
	if err != nil {
		return nil, err
	}

	if len(tarballs) != 1 {
		return nil, fmt.Errorf("%w, found %d", errNPMTarballCount, len(tarballs))
	}

	file, err := hashDependencyFile(source, ".", filepath.Base(tarballs[0]))
	if err != nil {
		return nil, err
	}

	return []builtFile{{source: filepath.Join(source, file.Path), file: file}}, nil
}

func distFiles(source string) ([]builtFile, error) {
	raw, err := dependencyFilesAt(source, ".")
	if err != nil {
		return nil, err
	}

	files := make([]builtFile, 0, len(raw))
	for _, file := range raw {
		files = append(files, builtFile{source: filepath.Join(source, file.Path), file: file})
	}

	return files, nil
}

// jvmFiles finds the files of a Maven or Gradle build where the release's
// build job takes them (build-maven.yml, build-gradle-app.yml): the direct
// children of the build directory of the project and of every module below
// it, target/*.jar and target/*.war for Maven, build/libs/*.jar for Gradle,
// of which the release keeps what domainrelease.IsReleaseArtifact accepts,
// so no original-*.jar. A file is kept under its module's path, the root
// module's at the top, so the modules of a reactor cannot collide. Gradle's
// buildSrc builds the build itself and is no module of the product.
func jvmFiles(planned pipeline.PlannedArtifact) ([]builtFile, error) {
	buildDir := "target"
	if planned.ProjectType == projecttype.Gradle {
		buildDir = "build/libs"
	}

	root, err := pathsafe.OpenRoot(planned.WorkingDirectory)
	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	files := []builtFile{}

	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "buildSrc" {
				return fs.SkipDir
			}

			return nil
		}

		directory := filepath.ToSlash(filepath.Dir(path))
		if directory != buildDir && !strings.HasSuffix(directory, "/"+buildDir) {
			return nil
		}

		extension := filepath.Ext(entry.Name())
		if !domainrelease.IsReleaseArtifact(entry.Name()) || extension != ".jar" && (extension != ".war" || planned.ProjectType != projecttype.Maven) {
			return nil
		}

		file, err := hashDependencyFile(planned.WorkingDirectory, ".", path)
		if err != nil {
			return err
		}

		file.Path = filepath.ToSlash(filepath.Join(strings.TrimSuffix(strings.TrimSuffix(directory, buildDir), "/"), entry.Name()))
		files = append(files, builtFile{source: filepath.Join(planned.WorkingDirectory, path), file: file})

		return nil
	})

	return files, err
}
