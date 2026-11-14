// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
)

// What a shipped demo admits: where its consumer checkout is, which events it
// has a route for on a lab forge, and what its GitLab gate runs in.

// demosDir holds the shipped demos, relative to the repository root.
const demosDir = "test/live/fixtures/consumers"

// demoConsumerDir is the consumer checkout a shipped demo is run from.
func demoConsumerDir(root, demo string) string {
	return filepath.Join(root, filepath.FromSlash(demosDir), demo, "consumer")
}

func isDir(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

// requireGitLabReleaseBuild refuses a GitLab release of a demo without the
// build the release policy's build job runs, GitLabConsumerBuildScript: the
// release commands are the adopter's, and a demo that ships none
// (test/live/fixtures/consumers/README.md) has no GitLab release to run.
func requireGitLabReleaseBuild(checkout string) error {
	info, err := os.Lstat(filepath.Join(checkout, GitLabConsumerBuildScript))
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("the demo has no %s, so no GitLab release: its release commands are the adopter's (test/live/fixtures/consumers/README.md): %w", GitLabConsumerBuildScript, errs.ErrValidation)
	}

	return nil
}

// demoGate is what a demo's GitLab gate runs in beside $CI_JOB_IMAGE: the
// NAME of each $CI_JOB_IMAGE_<NAME> it checks in, and whether it lints in
// $CI_LINT_IMAGE.
type demoGate struct {
	extraJobs []string
	lints     bool
}

// readDemoGate reads the demo's GitLab gate at path; a demo without one runs
// in nothing further.
func readDemoGate(path string) (demoGate, error) {
	body, err := os.ReadFile(path) //nolint:gosec // A demo of this repository, read as data.
	if errors.Is(err, fs.ErrNotExist) {
		return demoGate{}, nil
	}

	if err != nil {
		return demoGate{}, err
	}

	var gate demoGate
	if gate.extraJobs, err = gitlabGateExtraJobImages(body); err == nil {
		gate.lints, err = gitlabGateLints(body)
	}

	if err != nil {
		return demoGate{}, fmt.Errorf("%s: %w", path, err)
	}

	return gate, nil
}

// admitLifecycle refuses a lifecycle case whose demo has no
// development route, the lifecycle's development phase: a service snapshot
// of the demo's one container, which the snapshot-images policy builds from
// the declaration the demo's release builds its images from
// (ServiceSnapshotSource), on Forgejo through the harness's service
// development workflow, on GitLab through the component. The route is read
// from the demo's files, not its name, and the planner's refusal is the
// demo's. The case's settings are held to the lifecycle's rules where every
// case's settings are: ParseTrialSettings on Forgejo,
// ParseGitLabLifecycleSettings in the GitLab admission.
func admitLifecycle(provider, checkout string) error {
	if !isLabForge(provider) {
		return fmt.Errorf("%s runs on %s and %s: %w", TrialLifecycle, forgeForgejo, forgeGitLab, errs.ErrValidation)
	}

	source, err := ServiceSnapshotSource(checkout)
	if err != nil {
		return err
	}

	return serviceSnapshotAdmission(checkout, source)
}

// errNoDevelopmentRoute is a demo without the root Containerfile or the
// declaration a service snapshot is planned from: the live harness's scope.
var errNoDevelopmentRoute = errors.New("no service snapshot and no development route")

// serviceSnapshotAdmission refuses a demo whose source declaration the
// planner cannot plan as a service snapshot, the development route every
// forge's lifecycle shares; a demo without a root Containerfile or the
// declaration has none.
func serviceSnapshotAdmission(checkout, source string) error {
	if present, err := exists(filepath.Join(checkout, "Containerfile")); err != nil || !present {
		return fmt.Errorf("%s: the demo has no root Containerfile, so %w: %w", TrialLifecycle, errNoDevelopmentRoute, errs.ErrValidation)
	}

	body, err := os.ReadFile(filepath.Join(checkout, source)) //nolint:gosec // G304: a fixed file under the demo checkout the harness itself chose.
	if err != nil {
		return fmt.Errorf("%s: the demo has no %s, so %w: %w", TrialLifecycle, source, errNoDevelopmentRoute, errs.ErrValidation)
	}

	if _, _, err = serviceSnapshotDeclarationOverlay(string(body)); err != nil {
		return fmt.Errorf("%s: %s is not a service snapshot's declaration, so the demo has no development route: %w: %w", TrialLifecycle, source, err, errs.ErrValidation)
	}

	return nil
}

// admitSnapshot refuses a snapshot case whose demo has no
// development route over every planned image: the declaration the case
// names, or else the one its release builds its images from
// (ServiceSnapshotSource), which the planner must plan
// in the snapshot's scope (snapshotImagesScope), on Forgejo through the
// harness's service development workflow, which is pinned as the demo's
// Forgejo release workflow pins the layer, on GitLab through the component,
// which needs nothing of the demo's but the declaration. The snapshot
// releases nothing, so no release build is asked for.
func admitSnapshot(provider, checkout, declaration string) error {
	if !isLabForge(provider) {
		return fmt.Errorf("%s runs on %s and %s: %w", TrialSnapshot, forgeForgejo, forgeGitLab, errs.ErrValidation)
	}

	if _, err := snapshotImagesAdmission(checkout, declaration); err != nil {
		return err
	}

	if provider != forgeForgejo {
		return nil
	}

	if present, err := exists(filepath.Join(checkout, filepath.FromSlash(forgejoReleaseWorkflow))); err != nil || !present {
		return fmt.Errorf("%s: the demo has no %s to pin the harness's development workflow by, so %w: %w", TrialSnapshot, forgejoReleaseWorkflow, errNoDevelopmentRoute, errs.ErrValidation)
	}

	return nil
}

// snapshotImagesAdmission plans a declaration of the demo, its source
// declaration when none is named, in the snapshot's scope and returns the
// plan; the planner's or the scope's refusal is the demo's.
func snapshotImagesAdmission(checkout, source string) (snapshotimage.Plan, error) {
	if source == "" {
		var err error
		if source, err = ServiceSnapshotSource(checkout); err != nil {
			return snapshotimage.Plan{}, err
		}
	}

	body, err := os.ReadFile(filepath.Join(checkout, source)) //nolint:gosec // G304: a fixed file under the demo checkout the harness itself chose.
	if err != nil {
		return snapshotimage.Plan{}, fmt.Errorf("%s: the demo has no %s, so %w: %w", TrialSnapshot, source, errNoDevelopmentRoute, errs.ErrValidation)
	}

	_, plan, err := snapshotDeclarationOverlay(string(body), snapshotImagesScope)
	if err != nil {
		return snapshotimage.Plan{}, fmt.Errorf("%s: %s is not a snapshot's declaration, so the demo has no development route: %w: %w", TrialSnapshot, source, err, errs.ErrValidation)
	}

	return plan, nil
}
