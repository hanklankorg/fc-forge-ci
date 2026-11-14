// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"reflect"
	"strings"
	"testing"
)

func TestImageBuildResources(t *testing.T) {
	for _, tc := range []struct {
		cpu, memory, jobs string
		valid             bool
	}{
		{"", "", "", true}, {"1", "4", "", true}, {"2", "4096", "2", true},
		{"92233720368547", "8796093022207", "2147483647", true},
		{"92233720368548", "4096", "1", false}, {"2", "8796093022208", "1", false},
		{"2", "3", "1", false}, {"1", "4096", "2", false}, {"2", "4096", "2147483648", false},
		{"0", "4096", "1", false}, {"02", "4096", "1", false}, {"+2", "4096", "1", false},
		{"2; touch sentinel", "4096", "1", false}, {"2", "4g", "1", false},
		{" 2", "4096", "1", false}, {"2\n", "4096", "1", false}, {"-1", "4096", "1", false},
		{"2.0", "4096", "1", false}, {"2", "4096", "1e1", false},
	} {
		t.Run(strings.Join([]string{tc.cpu, tc.memory, tc.jobs}, "/"), func(t *testing.T) {
			r, err := ParseImageBuildResources(tc.cpu, tc.memory, tc.jobs)
			if (err == nil) != tc.valid {
				t.Fatalf("tuple %+v: %v", tc, err)
			}

			if err != nil && r != (ImageBuildResources{}) {
				t.Fatal("partial accepted tuple on failure")
			}
		})
	}

	r, err := ParseImageBuildResources("", "", "")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"--cpu-period", "100000", "--cpu-quota", "200000", "--memory", "4294967296", "--memory-swap", "4294967296", "--jobs", "1", "--retry", "0"}

	args, err := r.PodmanArgs()
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("argv: %q (%v)", args, err)
	}

	for _, mode := range []string{"docker", "enforced=true"} {
		if AdmitImageBuildResources(mode, "", "", "") == nil {
			t.Fatalf("admitted %q", mode)
		}
	}

	if err := AdmitImageBuildResources("default", "", "", ""); err != nil {
		t.Fatal(err)
	}

	if AdmitImageBuildResources("default", "2", "", "") == nil {
		t.Fatal("silently ignored budget")
	}

	for _, options := range [][]string{
		{"build-budget=required", "build-budget=default"},
		{"build-cpus=2", "compiler-jobs=1", "build-memory-mib=garbage"},
		{"build-cpus=2", "build-enforced=true"}, {"compiler-jobs="},
	} {
		if AdmitImageBuildOptions(options, func(string) string { return "" }) == nil {
			t.Fatalf("admitted options: %q", options)
		}
	}
}

func TestImageBuildResourceRenderingRejectsInvalidState(t *testing.T) {
	for _, r := range []ImageBuildResources{
		{}, {cpus: 2, memoryMiB: 4096}, {cpus: -1, memoryMiB: 4096, compilerJobs: 1},
		{cpus: 2, memoryMiB: 3, compilerJobs: 1}, {cpus: 1, memoryMiB: 4096, compilerJobs: 2},
		{cpus: 92233720368548, memoryMiB: 4096, compilerJobs: 1},
		{cpus: 2, memoryMiB: 8796093022208, compilerJobs: 1},
	} {
		if args, err := r.PodmanArgs(); err == nil || len(args) != 0 {
			t.Fatalf("invalid state rendered: %v %q %v", r, args, err)
		}
	}
}

func TestImageBuildInvocationPreservesInheritedRequirements(t *testing.T) {
	for _, tc := range []struct {
		env                                 map[string]string
		mode, cpu, memory, jobs, diagnostic string
	}{
		{map[string]string{"FORGE_CI_IMAGE_BUILD_BUDGET": "required"}, "default", "", "", "", "conflicting"},
		{map[string]string{"FORGE_CI_IMAGE_BUILD_CPUS": "2"}, "required", "1", "", "", "conflicting"},
		{map[string]string{"FORGE_CI_IMAGE_BUILD_CPUS": "2"}, "", "", "", "", "require build-budget"},
	} {
		err := AdmitImageBuildInvocation(tc.mode, tc.cpu, tc.memory, tc.jobs, func(k string) string { return tc.env[k] })
		if err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestImageBuildSettings(t *testing.T) {
	for _, tc := range []struct {
		options []string
		env     map[string]string
		want    []string
	}{
		{nil, nil, []string{"default", "", "", ""}},
		{[]string{"build-budget=required"}, nil, []string{"required", "2", "4096", "2", "--cpu-period", "100000", "--cpu-quota", "200000", "--memory", "4294967296", "--memory-swap", "4294967296", "--jobs", "1", "--retry", "0"}},
		{[]string{"compiler-jobs=1"}, map[string]string{"FORGE_CI_IMAGE_BUILD_BUDGET": "required", "FORGE_CI_IMAGE_BUILD_CPUS": "3", "FORGE_CI_IMAGE_BUILD_MEMORY_MIB": "2048"}, []string{"required", "3", "2048", "1", "--cpu-period", "100000", "--cpu-quota", "300000", "--memory", "2147483648", "--memory-swap", "2147483648", "--jobs", "1", "--retry", "0"}},
	} {
		got, err := ResolveImageBuildOptions(tc.options, func(key string) string { return tc.env[key] })
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q: got %q, %v; want %q", tc.options, got, err, tc.want)
		}
	}
}
