// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package runners names the inputs by which the forge layers choose their
// runners, and refuses a plan whose jobs would have none.
//
// Every runner input is named [role-]<keyword>[-<os>-<arch>]: the role prefix
// (none for ordinary jobs), the forge's own keyword (runner-tags on GitLab,
// runs-on on Forgejo and GitHub), and, for an input bound to one platform, the
// RELEASE_PLATFORMS name of that platform with "/" turned into "-". This
// package is the one table of those names; the forge layers and their guards
// derive theirs from Input.
package runners

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// Role is the kind of job a runner input chooses the runner for.
type Role string

// The runner roles.
const (
	// Ordinary is every job that needs no special runner.
	Ordinary Role = "ordinary"
	// Build is a privileged image build.
	Build Role = "build"
	// Signer is a job that holds the release signing credentials.
	Signer Role = "signer"
)

// Roles is every runner role, in table order.
func Roles() []Role {
	return []Role{Ordinary, Build, Signer}
}

// Platforms is every platform a runner input can be bound to, in the
// RELEASE_PLATFORMS spelling.
func Platforms() []string {
	return []string{"linux/amd64", "linux/arm64", "darwin/arm64"}
}

// Input is the name of the forge layer's input that chooses the runner for
// role, bound to platform, or to no one platform when platform is empty:
// Input(gitlab, Build, "linux/arm64") is build-runner-tags-linux-arm64. A
// forge whose layer fixes the signer runner has no signer input.
func Input(forge provider.ForgeAPI, role Role, platform string) (string, error) {
	keyword, err := forgeKeyword(forge)
	if err != nil {
		return "", err
	}

	if !slices.Contains(Roles(), role) {
		return "", fmt.Errorf("unknown runner role %q, want one of %v: %w", role, Roles(), errs.ErrUsage)
	}

	if role == Signer && forge.FixesSignerRunner() {
		return "", fmt.Errorf("%s fixes the signer runner and has no signer runner input: %w", forge, errs.ErrUsage)
	}

	name := keyword
	if role != Ordinary {
		name = string(role) + "-" + name
	}

	if platform == "" {
		return name, nil
	}

	if !slices.Contains(Platforms(), platform) {
		return "", fmt.Errorf("unknown runner platform %q, want one of %v: %w", platform, Platforms(), errs.ErrUsage)
	}

	return name + "-" + strings.ReplaceAll(platform, "/", "-"), nil
}

// forgeKeyword is the forge's own word for the runner field of a job.
func forgeKeyword(forge provider.ForgeAPI) (string, error) {
	if keyword := forge.RunnerKeyword(); keyword != "" {
		return keyword, nil
	}

	return "", fmt.Errorf("forge %q has no runner inputs: %w", forge, errs.ErrUsage)
}

// Need is one runner a plan's jobs run on: a role on one platform.
type Need struct {
	Role     Role
	Platform string
	// Jobs says which planned jobs need it, for the refusal, e.g. "image builds".
	Jobs string
}

// Inputs is the runner inputs a forge layer passed, by name, with the values
// it passed after its own defaults.
type Inputs struct {
	forge  provider.ForgeAPI
	values map[string]string
}

// ParseInputs reads name=value pairs of runner inputs a layer of forge
// passed. Every name must be one Input makes for that forge. No pairs is nil:
// the layer passes no runner inputs, and Admit checks nothing.
func ParseInputs(forge provider.ForgeAPI, pairs []string) (*Inputs, error) {
	if len(pairs) == 0 {
		return nil, nil //nolint:nilnil // nil Inputs is the documented "layer passes none".
	}

	known, err := Names(forge)
	if err != nil {
		return nil, err
	}

	values := make(map[string]string, len(pairs))

	for _, pair := range pairs {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("runner input %q is not name=value: %w", pair, errs.ErrUsage)
		}

		if !slices.Contains(known, name) {
			return nil, fmt.Errorf("%s has no runner input %q: %w", forge, name, errs.ErrUsage)
		}

		if _, seen := values[name]; seen {
			return nil, fmt.Errorf("runner input %s is passed twice: %w", name, errs.ErrUsage)
		}

		values[name] = value
	}

	return &Inputs{forge: forge, values: values}, nil
}

// Names is every runner input name a layer of forge can have, each role
// unbound and bound to each platform, in table order.
func Names(forge provider.ForgeAPI) ([]string, error) {
	if _, err := forgeKeyword(forge); err != nil {
		return nil, err
	}

	var names []string

	for _, role := range Roles() {
		if role == Signer && forge.FixesSignerRunner() {
			continue
		}

		for _, platform := range append([]string{""}, Platforms()...) {
			name, err := Input(forge, role, platform)
			if err != nil {
				return nil, err
			}

			names = append(names, name)
		}
	}

	return names, nil
}

// Admit refuses the needs whose input has no value. There is no fallback
// across platforms or roles: an arm64 build is not run on the amd64 runner.
// A default the layer applied is a value; an empty runner list is one only
// where emptyListChoosesRunner says so. A nil Inputs admits every need.
func (in *Inputs) Admit(needs []Need) error {
	if in == nil {
		return nil
	}

	var refusals []error

	seen := map[string]bool{}

	for _, need := range needs {
		name, err := Input(in.forge, need.Role, need.Platform)
		if err != nil {
			return err
		}

		if seen[name] || in.hasValue(name, need) {
			continue
		}

		seen[name] = true

		refusals = append(refusals, in.refusal(name, need))
	}

	return errors.Join(refusals...)
}

func (in *Inputs) hasValue(name string, need Need) bool {
	value, ok := in.values[name]
	if !ok || strings.TrimSpace(value) == "" {
		return false
	}

	var labels []string
	if json.Unmarshal([]byte(value), &labels) == nil && len(labels) == 0 {
		return emptyListChoosesRunner(in.forge, need)
	}

	return true
}

// emptyListChoosesRunner reports whether an empty runner list is a choice for
// need's input. It is only for GitLab's unsuffixed runner-tags and
// signer-runner-tags, where it means untagged. A platform-bound input binds
// its jobs to that platform, which an untagged job could leave; image builds
// need a runner that permits them; and on Forgejo and GitHub an empty label
// list selects no runner.
func emptyListChoosesRunner(forge provider.ForgeAPI, need Need) bool {
	return forge.EmptyRunnerListIsUntagged() && need.Platform == "" && need.Role != Build
}

// refusal names the input with no value and how to set it.
func (in *Inputs) refusal(name string, need Need) error {
	if need.Platform == "" {
		return fmt.Errorf("the plan has %s, but the %s runner input %s has no value: set %s to a JSON list that selects their runner: %w",
			need.Jobs, in.forge, name, name, errs.ErrUsage)
	}

	return fmt.Errorf("the plan has %s on %s, but the %s runner input %s has no value: set %s to a JSON list that selects a %s runner, or leave %s out of the plan: %w",
		need.Jobs, need.Platform, in.forge, name, name, need.Platform, need.Platform, errs.ErrUsage)
}
