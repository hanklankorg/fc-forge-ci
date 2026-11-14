// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// A prepared directory is the inputs of one commit, which
// `just test-live-prepare` writes: the images and demo checkouts the cases of
// the Go tables (PreparedCases, PreparedLanes) run on. This file reads its
// prepared.env as data, binds a run to the prepared commit and says which
// runtime images a prepare builds. docs/live-runs.md, Prepare and run, shows
// the use.

// PreparedEnv names the directory `just test-live-prepare` wrote: the images
// and archives built once from one commit, which a run then takes by digest.
const PreparedEnv = "FORGE_CI_LIVE_PREPARED"

// PreparedEnvFile is the prepared directory's KEY=value file.
const PreparedEnvFile = "prepared.env"

// preparedEnvHashEnv names the SHA-256 of the prepared.env a run took, which
// its header records as the prepared directory's identity: the hash alone,
// never the file's values.
const preparedEnvHashEnv = "FORGE_CI_LIVE_PREPARED_ENV_SHA256"

// preparedCommitKey names the commit a directory was prepared from.
const preparedCommitKey = "PREPARED_COMMIT"

// The lab forges, which the prepared lanes run on.
const (
	forgeForgejo = string(provider.ForgeForgejo)
	forgeGitLab  = string(provider.ForgeGitLab)
)

func isLabForge(forge string) bool {
	return forge == forgeForgejo || forge == forgeGitLab
}

// scenarioRuntimeTarget is the runtime target of FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE,
// the job image of the GitLab scenarios.
const scenarioRuntimeTarget = "go"

// The prepared.env keys of a nanolinter job image prepared with
// job-image=<flavor>:<image>, each followed by flavorEnvKey(flavor): its
// reference, a Forgejo case's CI_JOB_IMAGE, and its archive, which a GitLab
// case's gate lints in.
const (
	preparedJobImagePrefix    = "JOB_IMAGE_"
	preparedLintArchivePrefix = "LINT_IMAGE_ARCHIVE_"
)

// jobImageFlavor is a job-image= flavor, as the prepare accepts it.
var jobImageFlavor = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// preparedEnvKey is a key of prepared.env, demoName the name of a demo.
var (
	preparedEnvKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	demoName       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// flavorEnvKey is a flavor in prepared.env's keys: capitals, '-' as '_'.
func flavorEnvKey(flavor string) string {
	return strings.ToUpper(strings.ReplaceAll(flavor, "-", "_"))
}

// ReadPreparedEnvFile reads KEY=value lines, skipping blank lines and #
// comments. Keys are capitals, digits and underscores, each set once, to a
// value that is not empty. Nothing is evaluated.
func ReadPreparedEnvFile(path string) (map[string]string, error) {
	env := map[string]string{}

	err := eachLine(path, func(number int, line string) error {
		if line == "" || strings.HasPrefix(line, "#") {
			return nil
		}

		key, value, found := strings.Cut(line, "=")
		switch {
		case !found || !preparedEnvKey.MatchString(key):
			return fmt.Errorf("%s:%d: want KEY=value, KEY in capitals, digits and underscores: %w", path, number, errs.ErrValidation)
		case value == "" || strings.Contains(value, "\r"):
			return fmt.Errorf("%s:%d: %s is empty or not one line: %w", path, number, key, errs.ErrValidation)
		}

		if _, twice := env[key]; twice {
			return fmt.Errorf("%s:%d: %s is set twice: %w", path, number, key, errs.ErrValidation)
		}

		env[key] = value

		return nil
	})
	if err != nil {
		return nil, err
	}

	if env[preparedCommitKey] == "" {
		return nil, fmt.Errorf("%s names no %s: %w", path, preparedCommitKey, errs.ErrValidation)
	}

	return env, nil
}

// eachLine calls visit with each line of the file at path, numbered from 1.
func eachLine(path string, visit func(int, string) error) error {
	file, err := os.Open(path) //nolint:gosec // Operator-named input, read as data.
	if err != nil {
		return err
	}

	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for number := 1; scanner.Scan(); number++ {
		if strings.ContainsRune(scanner.Text(), 0) {
			return fmt.Errorf("%s:%d: a NUL byte: %w", path, number, errs.ErrValidation)
		}

		if err := visit(number, scanner.Text()); err != nil {
			return err
		}
	}

	return scanner.Err()
}

// CheckPreparedSource refuses a run that would test prepared images against
// other source than they were built from. head is the checkout's commit and
// changes its `git status --porcelain`: the prepared commit must be head and
// the tree must hold nothing else, or the run would carry that commit's name
// over different files.
func CheckPreparedSource(dir, head, changes string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%s must be absolute, got %q: %w", PreparedEnv, dir, errs.ErrValidation)
	}

	env, err := ReadPreparedEnvFile(filepath.Join(dir, PreparedEnvFile))
	if err != nil {
		return err
	}

	if env[preparedCommitKey] != head {
		return fmt.Errorf("%s was prepared for %s and this checkout is %s; check that commit out or prepare again: %w",
			dir, env[preparedCommitKey], head, errs.ErrValidation)
	}

	if changed := strings.TrimSpace(changes); changed != "" {
		first, _, _ := strings.Cut(changed, "\n")

		return fmt.Errorf("%s was prepared for %s and this checkout has changes on top of it (%s); commit or remove them, then prepare again: %w",
			dir, head, strings.TrimSpace(first), errs.ErrValidation)
	}

	return nil
}

// PreparedRuntimeTargets lists the runtime targets a prepare builds image
// archives of, from the repository at root, for what it is named to run: a
// forge, every lane of it, or a lane of PreparedLanes by name. A GitLab lane
// takes the go image when it runs scenarios, and for each of its cases, a
// scenario's consumer (PreparedScenarioCase) among them, the
// image of the case's flavor or its Runtime, every $CI_JOB_IMAGE_<TARGET> the
// gate of its demo checks in and, for a snapshot, which runs no gate, the
// toolchain family of each producer its plan builds. A Forgejo lane takes
// none. Each must be a target of containers/runtime/images.json. Every
// nanolinter image a case takes, a Forgejo case's job image and what a GitLab
// gate lints in, must be of one of flavors, the job-image= flavors the
// prepare publishes.
func PreparedRuntimeTargets(root string, names, flavors []string) ([]string, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("name what to prepare for: forgejo, gitlab or a lane: %w", errs.ErrValidation)
	}

	for _, flavor := range flavors {
		if !jobImageFlavor.MatchString(flavor) {
			return nil, fmt.Errorf("a job image flavor is lowercase letters, digits and hyphens, got %q: %w", flavor, errs.ErrValidation)
		}
	}

	body, err := os.ReadFile(filepath.Join(root, "containers", "runtime", "images.json")) //nolint:gosec // This repository's roster, read as data.
	if err != nil {
		return nil, err
	}

	var roster []struct {
		Target string `json:"target"`
	}
	if err = json.Unmarshal(body, &roster); err != nil {
		return nil, fmt.Errorf("containers/runtime/images.json: %w", err)
	}

	targets := map[string]bool{}

	for _, name := range names {
		lanes := slices.DeleteFunc(slices.Clone(PreparedLanes), func(lane PreparedLane) bool { return name != lane.Forge && name != lane.Name })
		if len(lanes) == 0 {
			return nil, fmt.Errorf("want forgejo, gitlab or a lane of them, got %q: %w", name, errs.ErrValidation)
		}

		for _, lane := range lanes {
			if err := laneRuntimeTargets(root, lane, flavors, targets); err != nil {
				return nil, err
			}
		}
	}

	for target := range targets {
		if !slices.ContainsFunc(roster, func(image struct {
			Target string `json:"target"`
		}) bool {
			return image.Target == target
		}) {
			return nil, fmt.Errorf("the lanes name the runtime target %q, which containers/runtime/images.json does not build: %w", target, errs.ErrValidation)
		}
	}

	return slices.Sorted(maps.Keys(targets)), nil
}

// laneRuntimeTargets adds the runtime targets a lane needs, for its cases of
// the table and its scenarios' (PreparedScenarioCase), and refuses a case
// whose nanolinter image is of a flavor not among flavors.
func laneRuntimeTargets(root string, lane PreparedLane, flavors []string, targets map[string]bool) error {
	if lane.Forge == forgeGitLab && len(lane.Scenarios) != 0 {
		targets[scenarioRuntimeTarget] = true
	}

	cases := slices.DeleteFunc(slices.Clone(PreparedCases(lane.Forge)), func(c PreparedCase) bool { return lane.Lane == "" || c.Lane != lane.Lane })

	for _, scenario := range lane.Scenarios {
		if c, takes := PreparedScenarioCase(lane.Forge, scenario); takes {
			cases = append(cases, c)
		}
	}

	for _, trialCase := range cases {
		// A GitLab snapshot runs no gate, so it lints in nothing.
		lints := lane.Forge == forgeForgejo || trialCase.Event != TrialSnapshot
		for _, flavor := range []string{trialCase.JobImage, trialCase.NodeJobImage} {
			if lints && flavor != "" && !slices.Contains(flavors, flavor) {
				return fmt.Errorf("%s: %s takes a nanolinter job image the prepare does not publish; add job-image=%s:<local image>: %w",
					lane.Name, trialCase.Name(), flavor, errs.ErrValidation)
			}
		}

		if lane.Forge != forgeGitLab {
			continue
		}

		targets[cmp.Or(trialCase.Runtime, gitLabRuntimeTarget[trialCase.JobImage])] = true

		further, err := gitLabCaseFurtherImages(demoConsumerDir(root, trialCase.Demo), trialCase)
		if err != nil {
			return fmt.Errorf("%s: %s: %w", lane.Name, trialCase.Name(), err)
		}

		for _, name := range further {
			targets[strings.ToLower(name)] = true
		}
	}

	return nil
}

// gitLabCaseFurtherImages names the runtime images a GitLab case on the demo
// at checkout runs in beside its own: the toolchain family of each producer
// a snapshot's plan builds, or every $CI_JOB_IMAGE_<NAME> the demo's gate
// checks in.
func gitLabCaseFurtherImages(checkout string, c PreparedCase) ([]string, error) {
	if c.Event == TrialSnapshot {
		plan, err := snapshotImagesAdmission(checkout, c.SnapshotDeclaration)
		if err != nil {
			return nil, err
		}

		return serviceSnapshotFamilies(plan), nil
	}

	gate, err := readDemoGate(filepath.Join(checkout, filepath.FromSlash(GitLabGateFile)))

	return gate.extraJobs, err
}
