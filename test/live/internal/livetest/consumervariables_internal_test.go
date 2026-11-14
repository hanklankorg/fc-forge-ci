// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestMergeConsumerVariablesBuildahOmitsOnlyTheOptionalEmptyImage(t *testing.T) {
	t.Parallel()

	plan := map[string]string{"CI_RUNNER_LABEL": "runner"}
	runtime := map[string]string{"CI_IMAGE_BUILDER": ImageBuilderBuildah, "CI_IMAGE_BUILDER_JOB_IMAGE": ""}
	beforePlan, beforeRuntime := maps.Clone(plan), maps.Clone(runtime)

	variables, err := MergeConsumerVariables(plan, runtime)
	if err != nil {
		t.Fatal(err)
	}

	if _, found := variables["CI_IMAGE_BUILDER_JOB_IMAGE"]; found || variables["CI_IMAGE_BUILDER"] != ImageBuilderBuildah || len(variables) != 2 {
		t.Fatalf("wrong fresh-repository bindings: %v", variables)
	}

	// Exercise Forgejo's required-value API contract entirely in memory: no
	// transport/provider, and no empty optional POST or deletion is generated.
	var posted []string

	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Value string `json:"value"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.Value == "" {
			w.WriteHeader(http.StatusUnprocessableEntity)

			return
		}

		posted = append(posted, r.URL.Path)

		w.WriteHeader(http.StatusCreated)
	})

	for _, name := range sortedKeys(variables) {
		body, marshalErr := json.Marshal(map[string]string{"value": variables[name]})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}

		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/repos/fixture/fresh/actions/variables/"+name, strings.NewReader(string(body)))
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)

		if response.Code != http.StatusCreated {
			t.Fatalf("invalid Actions variable POST: %s", name)
		}
	}

	if len(posted) != 2 || !maps.Equal(plan, beforePlan) || !maps.Equal(runtime, beforeRuntime) {
		t.Fatal("lost required POSTs or mutated the admitted input")
	}
}

func TestMergeConsumerVariablesKeepsNonemptyBuilderImages(t *testing.T) {
	t.Parallel()

	image := "registry.invalid/fixture/job@sha256:" + strings.Repeat("a", 64)
	for _, builder := range []string{ImageBuilderBuildah, ImageBuilderKaniko} {
		variables, err := MergeConsumerVariables(nil, map[string]string{"CI_IMAGE_BUILDER": builder, "CI_IMAGE_BUILDER_JOB_IMAGE": image})
		if err != nil || variables["CI_IMAGE_BUILDER_JOB_IMAGE"] != image || variables["CI_IMAGE_BUILDER"] != builder {
			t.Fatalf("changed nonempty %s image: %v, %v", builder, variables, err)
		}
	}
}

func TestMergeConsumerVariablesRejectsInvalidBindingsWithoutPartialResults(t *testing.T) {
	t.Parallel()

	for _, runtime := range []map[string]string{
		{"CI_IMAGE_BUILDER": ImageBuilderKaniko},
		{"CI_IMAGE_BUILDER": ImageBuilderKaniko, "CI_IMAGE_BUILDER_JOB_IMAGE": ""},
		{"CI_IMAGE_BUILDER": ImageBuilderKaniko, "CI_IMAGE_BUILDER_JOB_IMAGE": "registry.invalid/job:mutable"},
		{"CI_IMAGE_BUILDER": ""},
		{"CI_IMAGE_BUILDER": "unsupported"},
		{"CI_IMAGE_BUILDER": ImageBuilderBuildah, "CI_IMAGE_BUILDER_JOB_IMAGE": "", "CI_RUNNER_LABEL": ""},
		{"CI_IMAGE_BUILDER_JOB_IMAGE": ""},
		{"POLICY_INPUT": ""},
	} {
		before := maps.Clone(runtime)

		variables, err := MergeConsumerVariables(map[string]string{"REQUIRED": "private-value"}, runtime)
		if variables != nil || !errors.Is(err, errs.ErrValidation) || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("invalid binding returned partial values or no safe refusal: %v, %v", variables, err)
		}

		if !maps.Equal(runtime, before) {
			t.Fatal("validation mutated runtime bindings")
		}
	}
}

func TestMergeConsumerVariablesDoesNotUnsetAnExistingPlanBinding(t *testing.T) {
	t.Parallel()

	plan := map[string]string{"CI_IMAGE_BUILDER_JOB_IMAGE": "registry.invalid/owned@sha256:" + strings.Repeat("a", 64)}
	before := maps.Clone(plan)

	got, err := MergeConsumerVariables(plan, map[string]string{"CI_IMAGE_BUILDER": ImageBuilderBuildah, "CI_IMAGE_BUILDER_JOB_IMAGE": ""})
	if got != nil || !errors.Is(err, errs.ErrValidation) || !maps.Equal(plan, before) {
		t.Fatalf("empty runtime binding erased existing authority: %v, %v", got, err)
	}

	if got, err = MergeConsumerVariables(map[string]string{"POLICY_INPUT": ""}, nil); got != nil || !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("empty required plan variable was silently omitted: %v, %v", got, err)
	}
}

func TestChooseImageBuilderRequiresKanikoArchiveBeforeTargetUse(t *testing.T) {
	t.Setenv(liveProfileEnv, profileFull)
	t.Setenv(ForgejoImageBuilderEnv, ImageBuilderKaniko)
	t.Setenv(KanikoJobImageArchiveEnv, "")

	targetUsed := false

	defer func() {
		failure, ok := recover().(acceptanceFailure)
		if !ok || !strings.Contains(string(failure), KanikoJobImageArchiveEnv) || targetUsed {
			t.Errorf("missing archive did not refuse before target use: %v", failure)
		}
	}()

	_ = ChooseImageBuilder(stopOnFatalTB{TB: t}, Target{Forge: provider.ForgeForgejo})
	targetUsed = true
}

func TestMergeConsumerVariablesPreservesAuthority(t *testing.T) {
	plan := map[string]string{"FORGE_CI_REPOSITORY": "fixtures/engine", "CI_RUNNER_LABEL": "runner"}

	got, err := MergeConsumerVariables(plan, map[string]string{"FORGE_CI_REPOSITORY": "fixtures/engine", "SERVICE_IMAGE_REPOSITORY": "registry.invalid/fixtures/service"})
	if err != nil || len(got) != 3 || got["FORGE_CI_REPOSITORY"] != "fixtures/engine" {
		t.Fatal("same binding was not deduplicated")
	}

	got["CI_RUNNER_LABEL"] = "changed"

	if plan["CI_RUNNER_LABEL"] != "runner" {
		t.Fatal("mutated admitted plan")
	}

	if _, err := MergeConsumerVariables(plan, map[string]string{"FORGE_CI_REPOSITORY": "other/engine"}); err == nil {
		t.Fatal("conflicting repository authority accepted")
	}
}

func TestMergeConsumerVariablesRefusesConflictingBuilderWithoutPartialBindings(t *testing.T) {
	t.Parallel()

	plan := map[string]string{"CI_IMAGE_BUILDER": "buildah"}
	runtime := map[string]string{"A_RUNTIME_BINDING": "private-value", "CI_IMAGE_BUILDER": "kaniko"}
	got, err := MergeConsumerVariables(plan, runtime)

	if got != nil || !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "CI_IMAGE_BUILDER") {
		t.Fatalf("conflicting builder did not refuse the complete binding: %v, %v", got, err)
	}

	if strings.Contains(err.Error(), "buildah") || strings.Contains(err.Error(), "kaniko") || strings.Contains(err.Error(), "private-value") {
		t.Fatal("conflict error exposed a variable value")
	}

	if len(plan) != 1 || len(runtime) != 2 || runtime["CI_IMAGE_BUILDER"] != "kaniko" {
		t.Fatal("failed merge mutated its inputs")
	}
}
