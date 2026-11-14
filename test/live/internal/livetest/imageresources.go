// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const resourceDefaultMode = "default"

// ImageBuildResources describes local preparation, not a production job's limits.
// MiB is 1048576 bytes. Jobs is bounded by Cargo's signed 32-bit jobs interface.
type ImageBuildResources struct {
	cpus, memoryMiB, compilerJobs int64
}

// ParseImageBuildResources accepts canonical positive decimal integers only.
// An omitted tuple defaults to 2/4096/2; omitted jobs follows the selected CPUs.
// Empty positional fields retain their defaults. No accepted values escape on error.
func ParseImageBuildResources(cpus, memory, jobs string) (ImageBuildResources, error) {
	parse := func(name, value string, fallback, maximum int64) (int64, error) {
		if value == "" {
			return fallback, nil
		}

		if value[0] < '1' || value[0] > '9' {
			return 0, fmt.Errorf("%w: %s: want canonical positive decimal integer", errs.ErrValidation, name)
		}

		for _, char := range value {
			if char < '0' || char > '9' {
				return 0, fmt.Errorf("%w: %s: want canonical positive decimal integer", errs.ErrValidation, name)
			}
		}

		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || number > maximum {
			return 0, fmt.Errorf("%w: %s: exceeds supported signed interface range", errs.ErrValidation, name)
		}

		return number, nil
	}

	var (
		resources ImageBuildResources
		err       error
	)
	if resources.cpus, err = parse("build-cpus", cpus, 2, math.MaxInt64/100000); err != nil {
		return ImageBuildResources{}, err
	}

	if resources.memoryMiB, err = parse("build-memory-mib", memory, 4096, math.MaxInt64/1048576); err != nil {
		return ImageBuildResources{}, err
	}

	if resources.compilerJobs, err = parse("compiler-jobs", jobs, resources.cpus, math.MaxInt32); err != nil {
		return ImageBuildResources{}, err
	}

	if resources.compilerJobs > math.MaxInt32 || resources.compilerJobs > resources.cpus {
		return ImageBuildResources{}, fmt.Errorf("%w: compiler-jobs exceeds CPUs or signed compiler range", errs.ErrValidation)
	}
	// Podman's minimum build memory is 4 MiB; lower values cannot be admitted.
	if resources.memoryMiB < 4 {
		return ImageBuildResources{}, fmt.Errorf("%w: build-memory-mib must be at least 4", errs.ErrValidation)
	}

	return resources, nil
}

// PodmanArgs rejects invalid/zero state before rendering. This is NOT admission.
func (resources ImageBuildResources) PodmanArgs() ([]string, error) {
	if resources.cpus <= 0 || resources.cpus > math.MaxInt64/100000 || resources.memoryMiB < 4 || resources.memoryMiB > math.MaxInt64/1048576 || resources.compilerJobs <= 0 || resources.compilerJobs > math.MaxInt32 || resources.compilerJobs > resources.cpus {
		return nil, fmt.Errorf("%w: invalid image build resource state", errs.ErrValidation)
	}

	return []string{"--cpu-period", "100000", "--cpu-quota", strconv.FormatInt(resources.cpus*100000, 10), "--memory", strconv.FormatInt(resources.memoryMiB*1048576, 10), "--memory-swap", strconv.FormatInt(resources.memoryMiB*1048576, 10), "--jobs", "1", "--retry", "0"}, nil
}

// AdmitImageBuildInvocation resolves inherited requirements and positional argv
// once, at the admission owner. Omission inherits; explicit conflicting values
// refuse, including an explicit default attempting to clear inherited required.
func AdmitImageBuildInvocation(mode, cpus, memory, jobs string, getenv func(string) string) error {
	_, err := ResolveImageBuildInvocation(mode, cpus, memory, jobs, getenv)

	return err
}

// ResolveImageBuildInvocation returns canonical settings, not an enforcement
// certificate. Required means the build owner must apply the native arguments.
func ResolveImageBuildInvocation(mode, cpus, memory, jobs string, getenv func(string) string) ([]string, error) {
	values := []string{mode, cpus, memory, jobs}

	for index, key := range []string{"FORGE_CI_IMAGE_BUILD_BUDGET", "FORGE_CI_IMAGE_BUILD_CPUS", "FORGE_CI_IMAGE_BUILD_MEMORY_MIB", "FORGE_CI_IMAGE_BUILD_COMPILER_JOBS"} {
		inherited := getenv(key)
		if values[index] != "" && inherited != "" && values[index] != inherited {
			return nil, fmt.Errorf("%w: conflicting image build resource input: %s", errs.ErrValidation, key)
		}

		if values[index] == "" {
			values[index] = inherited
		}
	}

	if values[0] == "" {
		values[0] = resourceDefaultMode
	}

	if err := AdmitImageBuildResources(values[0], values[1], values[2], values[3]); err != nil {
		return nil, err
	}

	if values[0] == resourceDefaultMode {
		return values, nil
	}

	resources, err := ParseImageBuildResources(values[1], values[2], values[3])
	if err != nil {
		return nil, err
	}

	args, err := resources.PodmanArgs()
	if err != nil {
		return nil, err
	}

	return append([]string{"required", strconv.FormatInt(resources.cpus, 10), strconv.FormatInt(resources.memoryMiB, 10), strconv.FormatInt(resources.compilerJobs, 10)}, args...), nil
}

// AdmitImageBuildResources preserves explicit legacy default callers. Required
// validates a supported native-limit request; actual enforcement is unmeasured.
func AdmitImageBuildResources(mode, cpus, memory, jobs string) error {
	switch mode {
	case resourceDefaultMode:
		if cpus != "" || memory != "" || jobs != "" {
			return fmt.Errorf("%w: resource values require build-budget=required", errs.ErrValidation)
		}

		return nil
	case "required":
		_, err := ParseImageBuildResources(cpus, memory, jobs)

		return err
	default:
		return fmt.Errorf("%w: build-budget: want default or required", errs.ErrValidation)
	}
}

// AdmitImageBuildOptions is the prepare's sole resource-option reader.
func AdmitImageBuildOptions(options []string, getenv func(string) string) error {
	_, err := ResolveImageBuildOptions(options, getenv)

	return err
}

// ResolveImageBuildOptions reads the prepare's options and resolves inherited settings.
func ResolveImageBuildOptions(options []string, getenv func(string) string) ([]string, error) {
	values := map[string]string{}
	seen := map[string]bool{}

	for _, option := range options {
		key, value, ok := strings.Cut(option, "=")
		if !ok || (key != "build-budget" && key != "build-cpus" && key != "build-memory-mib" && key != "compiler-jobs") {
			return nil, fmt.Errorf("%w: unknown image build resource option", errs.ErrValidation)
		}

		if seen[key] || value == "" {
			return nil, fmt.Errorf("%w: duplicate or empty image build resource option: %s", errs.ErrValidation, key)
		}

		seen[key], values[key] = true, value
	}

	return ResolveImageBuildInvocation(values["build-budget"], values["build-cpus"], values["build-memory-mib"], values["compiler-jobs"], getenv)
}
