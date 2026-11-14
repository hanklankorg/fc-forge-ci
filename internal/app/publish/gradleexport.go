// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// GradlePublicationExporter invokes the existing project-local wrapper with an
// explicit output location. It needs no release-signing or publishing secrets.
type GradlePublicationExporter interface {
	RunInDirEnvInherit(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, args ...string) error
}

// GradleExportInput selects the prepared project and a fresh publication output.
type GradleExportInput struct {
	Directory, Output, Version string
	Project                    string // optional exact Gradle project path; empty exports the entire build
	Central                    bool
	Snapshot                   bool
}

const gradlePublicationInit = `import org.gradle.api.publish.PublishingExtension
import org.gradle.api.publish.maven.MavenPublication
allprojects {
    pluginManager.withPlugin("maven-publish") {
        extensions.configure<PublishingExtension> {
            repositories {
                maven {
                    name = "ForgeCIExport"
                    url = java.io.File(System.getenv("FORGE_CI_PUBLICATION_DIR")).toURI()
                }
            }
        }
    }
}
gradle.projectsEvaluated {
    val selected = System.getenv("FORGE_CI_PUBLICATION_PROJECT")
    val projects = if (selected.isNullOrEmpty()) rootProject.allprojects else setOf(rootProject.project(selected))
    val publications = projects.flatMap {
        it.extensions.findByType(PublishingExtension::class.java)?.publications
            ?.withType(MavenPublication::class.java)?.toList() ?: emptyList()
    }
    require(publications.isNotEmpty()) { "Apply maven-publish and declare a JVM Maven publication" }
    val expected = System.getenv("FORGE_CI_PUBLICATION_VERSION")
    val snapshot = System.getenv("FORGE_CI_PUBLICATION_SNAPSHOT") == "true"
    publications.forEach {
        if (snapshot) require(it.version.matches(Regex("[0-9]+\\.[0-9]+\\.[0-9]+-SNAPSHOT"))) { "Every snapshot publication must have a MAJOR.MINOR.PATCH-SNAPSHOT version" }
        if (!expected.isNullOrEmpty()) require(it.version == expected) { "Every publication must have the prepared release version $expected" }
    }
    val coordinates = publications.map { "${it.groupId}:${it.artifactId}:${it.version}" }
    require(coordinates.distinct().size == coordinates.size) { "Duplicate Maven publication coordinates are forbidden" }
}
`

// ExportGradlePublication exports and validates in private staging. Existing
// nonempty outputs refuse before Gradle runs; a failed build installs no files.
func ExportGradlePublication(ctx context.Context, ops GradlePublicationExporter, out, stderr io.Writer, in GradleExportInput) (result error) {
	if err := ValidateGradleExport(in); err != nil {
		return err
	}

	stage, err := pathsafe.NewArtifactStaging(in.Output)
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, stage.Close()) }()

	init, err := os.CreateTemp("", "forge-ci-publication-*.init.gradle.kts")
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, os.Remove(init.Name())) }()

	_, writeErr := io.WriteString(init, gradlePublicationInit)
	if closeErr := errors.Join(writeErr, init.Close()); closeErr != nil {
		return closeErr
	}

	output, err := filepath.Abs(stage.Root().Name())
	if err != nil {
		return err
	}

	env := []string{"FORGE_CI_PUBLICATION_DIR=" + output, "FORGE_CI_PUBLICATION_VERSION=" + in.Version, "FORGE_CI_PUBLICATION_PROJECT=" + in.Project, fmt.Sprintf("FORGE_CI_PUBLICATION_SNAPSHOT=%t", in.Snapshot)}

	args := gradleExportArgs(init.Name(), output, in.Project, in.Snapshot)

	if runErr := ops.RunInDirEnvInherit(ctx, in.Directory, env, out, stderr, args...); runErr != nil {
		return runErr
	}

	set, err := inspectMavenPublication(output, in.Version, in.Central, in.Snapshot)
	if err != nil {
		return err
	}

	if installErr := stage.Install(); installErr != nil {
		return installErr
	}

	_, err = fmt.Fprintf(out, "Exported %d Maven publication(s) into %s\n", len(set.Publications), in.Output)

	return err
}

// ValidateGradleExport checks static inputs before the preceding build phase.
// It invokes no tool, creates no directory and reads no credential. The wrapper
// may not yet be executable: the preceding Gradle build sets its execute bits.
func ValidateGradleExport(in GradleExportInput) error {
	valid, _ := regexp.MatchString(`^(:|:?[A-Za-z0-9_][A-Za-z0-9_.-]*(:[A-Za-z0-9_][A-Za-z0-9_.-]*)*)?$`, in.Project)
	if !valid {
		return fmt.Errorf("publication project must be an exact Gradle project path (for example :lib): %w", errs.ErrUsage)
	}

	if err := validateGradleExportVersion(in); err != nil {
		return err
	}

	if in.Output == "" {
		return fmt.Errorf("publication output directory is required: %w", errs.ErrUsage)
	}

	if entries, err := os.ReadDir(in.Output); err == nil && len(entries) != 0 {
		return fmt.Errorf("publication output is not empty; choose a fresh directory: %w", errs.ErrValidation)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if info, err := os.Stat(filepath.Join(in.Directory, "gradlew")); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("publication export requires a regular project gradlew file: %w", errs.ErrMissingInput)
	}

	return nil
}

func gradleExportArgs(init, output, project string, snapshot bool) []string {
	args := []string{"--no-daemon", "--init-script", init}

	prefix := ""
	if project != "" {
		prefix = ":" + strings.Trim(project, ":")
		if prefix != ":" {
			prefix += ":"
		}
	}

	if snapshot {
		// Maven-local export retains base SNAPSHOT names; the native uploader
		// owns timestamp allocation and merging the registry's metadata.
		return append(args, "-Dmaven.repo.local="+output, prefix+"publishToMavenLocal")
	}

	return append(args, prefix+"publishAllPublicationsToForgeCIExportRepository")
}

func validateGradleExportVersion(in GradleExportInput) error {
	if in.Snapshot && in.Version == "" {
		return nil
	}

	if err := domainpublish.ValidateMavenVersion(in.Version, in.Snapshot); err != nil {
		return fmt.Errorf("publication version: %w: %w", err, errs.ErrInvalidConfig)
	}

	return nil
}
