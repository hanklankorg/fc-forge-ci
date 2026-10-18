// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
	"github.com/diggsweden/forge-ci/internal/secrettext"
)

// MavenFileDeployer runs a pinned deploy-file goal in an empty, owned directory.
// It never loads the consumer's project, plugins or .mvn configuration.
type MavenFileDeployer interface {
	RunInheritIn(ctx context.Context, dir string, stdout, stderr io.Writer, args ...string) error
}

// UploadMavenSnapshots delegates timestamp allocation and repository metadata
// merging to Maven's native resolver, retaining artifact-first publication.
func UploadMavenSnapshots(ctx context.Context, ops MavenFileDeployer, registry provider.ForgeMavenRegistry, out io.Writer, directory string) (result error) {
	commands, err := snapshotDeploymentCommands(directory)
	if err != nil {
		return err
	}

	if registry.Token == "" || registry.ServerID == "" {
		return fmt.Errorf("snapshot repository credentials are required: %w", errs.ErrPermissionDenied)
	}

	work, err := os.MkdirTemp("", "forge-ci-snapshot-publish-*")
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, os.RemoveAll(work)) }()

	settings, cleanup, err := writeTempSecretFile("forge-ci-snapshot-settings-*", registry.RenderSettingsXML())
	if err != nil {
		return err
	}
	defer cleanup()

	diagnostics := secrettext.NewWriter(out, registry.Token)
	defer func() { result = errors.Join(result, diagnostics.Close()) }()

	for index, args := range commands {
		args = append(args, "--settings", settings, "-DrepositoryId="+registry.ServerID, "-Durl="+registry.URL)
		if deployErr := ops.RunInheritIn(ctx, work, diagnostics, diagnostics, args...); deployErr != nil {
			return fmt.Errorf("snapshot deployment stopped after %d/%d publications; inspect repository metadata before retrying: %w", index, len(commands), secrettext.RedactError(deployErr, registry.Token))
		}
	}

	_, err = fmt.Fprintf(out, "Uploaded %d snapshot publication(s); Maven resolved timestamped versions and repository metadata\n", len(commands))

	return err
}

func snapshotDeploymentCommands(directory string) ([][]string, error) {
	set, err := InspectMavenSnapshot(directory, "")
	if err != nil {
		return nil, err
	}

	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}

	if strings.Contains(absolute, ",") {
		return nil, fmt.Errorf("publication directory cannot contain a comma for Maven deploy-file: %w", errs.ErrUsage)
	}

	commands := make([][]string, 0, len(set.Publications))
	for _, publication := range set.Publications {
		args, argsErr := snapshotDeploymentArgs(absolute, publication, set.Payloads)
		if argsErr != nil {
			return nil, argsErr
		}

		commands = append(commands, args)
	}

	return commands, nil
}

func snapshotDeploymentArgs(directory string, publication domainpublish.MavenPublication, payloads []string) ([]string, error) {
	stem := path.Join(publication.Directory(), publication.Stem())
	main := stem + "." + publication.Packaging
	args := []string{"--batch-mode", "--no-transfer-progress", "org.apache.maven.plugins:maven-deploy-plugin:3.2.0:deploy-file",
		"-Dmaven.resolver.transport=native", "-Daether.connector.http.followRedirects=false", "-Daether.connector.http.retryHandler.count=0", "-Daether.connector.http.expectContinue=false",
		"-DretryFailedDeploymentCount=1", "-Dmaven.deploy.file.skip=releases",
		"-Dfile=" + filepath.Join(directory, filepath.FromSlash(main)), "-DpomFile=" + filepath.Join(directory, filepath.FromSlash(stem+".pom")),
		"-DgroupId=" + publication.GroupID, "-DartifactId=" + publication.ArtifactID, "-Dversion=" + publication.Version, "-Dpackaging=" + publication.Packaging}

	var files, classifiers, types []string

	for _, name := range payloads {
		if path.Dir(name) != publication.Directory() || name == main || name == stem+".pom" {
			continue
		}

		if strings.Contains(name, ",") {
			return nil, fmt.Errorf("publication filenames cannot contain commas for Maven deploy-file: %w", errs.ErrValidation)
		}

		extension := path.Ext(name)

		classifier := strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(name, stem), extension), "-")
		if classifier == "" {
			classifier = " "
		} // Maven distinguishes an empty property from an omitted one; the plugin trims each classifier.

		files = append(files, filepath.Join(directory, filepath.FromSlash(name)))
		classifiers = append(classifiers, classifier)
		types = append(types, strings.TrimPrefix(extension, "."))
	}

	if len(files) > 0 {
		args = append(args, "-Dfiles="+strings.Join(files, ","), "-Dclassifiers="+strings.Join(classifiers, ","), "-Dtypes="+strings.Join(types, ","))
	}

	return args, nil
}
