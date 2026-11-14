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
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// ValidateMavenDeployment checks every project's resolved version. The caller
// supplies either the secret-free preflight or the actual deployment environment.
func ValidateMavenDeployment(ctx context.Context, ops MavenOps, stderr io.Writer, in MavenCentralDeployInput, snapshot bool, expected string) (result error) {
	if err := validateMavenModelOptions(in.CLIOpts); err != nil {
		return err
	}

	directory, err := os.MkdirTemp("", "forge-ci-maven-model-*")
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, os.RemoveAll(directory)) }()

	args := append([]string{}, in.CLIOpts...)
	if in.SettingsPath != "" {
		args = append(args, "--settings", in.SettingsPath)
	}

	if in.Profile != "" {
		args = append(args, "-P"+in.Profile)
	}

	args = append(args, "-DskipTests", "org.apache.maven.plugins:maven-help-plugin:3.5.1:effective-pom", "-Doutput="+filepath.Join(directory, "effective.xml"))
	if runErr := ops.RunInherit(ctx, io.Discard, stderr, args...); runErr != nil {
		return runErr
	}

	body, err := readEffectiveMavenPOM(directory)
	if err != nil {
		return err
	}

	return domainpublish.ValidateMavenReactorVersions(body, snapshot, expected)
}

func readEffectiveMavenPOM(directory string) ([]byte, error) {
	root, err := pathsafe.OpenRoot(directory)
	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	info, err := root.Lstat("effective.xml")
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, fmt.Errorf("effective POM must be a bounded regular file: %w", errs.ErrValidation)
	}

	return root.ReadFile("effective.xml")
}

// Model and deployment must resolve the same project/profile/properties. Extra
// goals cannot run during model inspection, nor may an option hide a later goal.
func validateMavenModelOptions(options []string) error {
	for _, option := range options {
		switch option {
		case "-B", "--batch-mode", "-ntp", "--no-transfer-progress", "-e", "--errors", "--fail-at-end", "-fae", "-q", "--quiet", "-U", "--update-snapshots":
			continue
		default:
			if strings.HasPrefix(option, "-D") && len(option) > 2 {
				continue
			}

			return fmt.Errorf("maven deployment cli-opts accepts diagnostic flags and -Dproperties only; select settings/profile through their dedicated flags: %w", errs.ErrUsage)
		}
	}

	return nil
}
