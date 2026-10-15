// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
)

func TestGenerateProvenance_CarriesIdentifiersAndDeps(t *testing.T) {
	t.Parallel()

	body, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
		Checksums:     strings.NewReader(strings.Repeat("a", 64) + "  dist/app.tar.gz\n"),
		GoSum:         strings.NewReader("github.com/foo/bar v1.0.0 h1:xyz=\n"),
		RepositoryURL: "https://codeberg.org/itiquette/repo",
		Ref:           "v2.0.0",
		SHA:           strings.Repeat("d", 40),
		BuilderID:     "https://codeberg.org/itiquette/repo/release.yml@v2.0.0",
		InvocationID:  "https://codeberg.org/itiquette/repo/actions/runs/777",
		StartedOn:     "2026-06-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := decodeStatement(t, body)

	if id := got.Predicate.RunDetails.Builder.ID; id != "https://codeberg.org/itiquette/repo/release.yml@v2.0.0" {
		t.Errorf("builder id = %q", id)
	}

	if id := got.Predicate.RunDetails.Metadata.InvocationID; id != "https://codeberg.org/itiquette/repo/actions/runs/777" {
		t.Errorf("invocation id = %q", id)
	}

	// externalParameters.source is where a verifier reads which repository
	// the artifact claims to come from, and it is composed here rather than
	// in the domain. Nothing asserted it: the whole suite passed with this
	// hardcoded to a foreign repository.
	ext := got.Predicate.BuildDefinition.ExternalParameters
	if src, ref := string(ext["source"]), string(ext["ref"]); src != `"git+https://codeberg.org/itiquette/repo"` || ref != `"v2.0.0"` {
		t.Errorf("externalParameters source = %s, ref = %s", src, ref)
	}

	// Source dep first, then the parsed go.sum module. A count alone would
	// not notice the commit digest going missing or the ref dropping out of
	// the source URI, and this statement is what a verifier trusts.
	deps := got.Predicate.BuildDefinition.ResolvedDependencies
	if len(deps) != 2 {
		t.Fatalf("got %d resolved deps, want 2 (source + 1 module)", len(deps))
	}

	if source := decodeDependency(t, deps[0]); source.URI != "git+https://codeberg.org/itiquette/repo@v2.0.0" || source.Digest["gitCommit"] != strings.Repeat("d", 40) {
		t.Errorf("source dep = %+v", source)
	}

	// That the module carries through at all is the claim here. How a
	// go.sum line becomes a purl is provenance.ParseGoSum's rule and is
	// pinned by TestParseGoSum in internal/domain/provenance.
	if module := decodeDependency(t, deps[1]); module.URI != "pkg:golang/github.com/foo/bar@v1.0.0" || module.Digest["gomod_h1"] != "xyz" {
		t.Errorf("module dep = %+v", module)
	}
}

// TestGenerateProvenance_RejectsMalformedSHA pins the fail-closed commit
// digest rule: the SHA lands in the attested resolvedDependencies, so junk
// must be refused here, not signed into evidence (callers no longer
// pre-validate in shell).
func TestGenerateProvenance_ForgejoProfileBuilderJoinsRepositoryAndWorkflowPath(t *testing.T) {
	t.Parallel()

	for workflow, want := range map[string]string{
		"release.yml":                    "https://codeberg.org/o/r/.forgejo/workflows/release.yml@v1.2.3",
		".forgejo/workflows/release.yml": "https://codeberg.org/o/r/.forgejo/workflows/release.yml@v1.2.3",
	} {
		body, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
			Checksums:     strings.NewReader(strings.Repeat("a", 64) + "  app.tar.gz\n"),
			RepositoryURL: "https://codeberg.org/o/r/",
			Ref:           "v1.2.3",
			SHA:           strings.Repeat("2", 40),
			StartedOn:     "2026-06-01T00:00:00Z",
			Profile:       apprelease.ProvenanceProfileForgejoActions,
			Workflow:      workflow,
		})
		if err != nil {
			t.Fatal(err)
		}

		if id := decodeStatement(t, body).Predicate.RunDetails.Builder.ID; id != want {
			t.Errorf("workflow %q: builder id = %q, want %q", workflow, id, want)
		}
	}
}

// The source ref name is held to the git ref-name grammar wherever it came
// from, beside the commit, before anything is parsed or built.
func TestGenerateProvenance_RejectsMalformedRefName(t *testing.T) {
	t.Parallel()

	for _, ref := range []string{"", "v1.2.3~1", "a..b", "v1 2", "v1\x1b", "-v1/", "v1.lock"} {
		_, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
			Checksums:     strings.NewReader(strings.Repeat("b", 64) + "  x\n"),
			RepositoryURL: "https://git.example/o/r",
			Ref:           ref,
			SHA:           strings.Repeat("2", 40),
			BuilderID:     "https://git.example/o/r/release.yml@v1",
			InvocationID:  "https://git.example/o/r/actions/runs/1",
			StartedOn:     "2026-06-01T00:00:00Z",
		})
		if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "is not a git ref name") {
			t.Errorf("ref %q: err = %v, want the ref name refused as a usage error", ref, err)
		}
	}
}

// Subjects come from a checksums file or from a typed list, and from exactly
// one of them.
func TestGenerateProvenance_SubjectsFromAListOrFromChecksums(t *testing.T) {
	t.Parallel()

	input := func(mutate func(*apprelease.ProvenanceInput)) apprelease.ProvenanceInput {
		in := apprelease.ProvenanceInput{
			RepositoryURL: "https://git.example/o/r",
			Ref:           "v1",
			SHA:           strings.Repeat("2", 40),
			BuilderID:     "https://git.example/o/r/release.yml@v1",
			InvocationID:  "https://git.example/o/r/actions/runs/1",
			StartedOn:     "2026-06-01T00:00:00Z",
		}
		mutate(&in)

		return in
	}

	list := []provenance.Subject{
		{Name: "registry.example/o/r-base", SHA256: strings.Repeat("a", 64)},
		{Name: "registry.example/o/r-base", SHA256: strings.Repeat("b", 64)},
	}
	checksums := strings.Repeat("a", 64) + "  registry.example/o/r-base\n" + strings.Repeat("b", 64) + "  registry.example/o/r-base\n"

	fromList, err := apprelease.GenerateProvenance(input(func(in *apprelease.ProvenanceInput) { in.Subjects = list }))
	if err != nil {
		t.Fatal(err)
	}

	fromChecksums, err := apprelease.GenerateProvenance(input(func(in *apprelease.ProvenanceInput) { in.Checksums = strings.NewReader(checksums) }))
	if err != nil {
		t.Fatal(err)
	}

	if string(fromList) != string(fromChecksums) || !strings.Contains(string(fromList), strings.Repeat("b", 64)) {
		t.Errorf("statement from a subject list differs from the one parsed from checksums:\n%s\n%s", fromList, fromChecksums)
	}

	for name, mutate := range map[string]func(*apprelease.ProvenanceInput){
		"neither": func(*apprelease.ProvenanceInput) {},
		"both": func(in *apprelease.ProvenanceInput) {
			in.Subjects, in.Checksums = list, strings.NewReader(checksums)
		},
	} {
		if _, err := apprelease.GenerateProvenance(input(mutate)); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("%s: err = %v, want ErrUsage", name, err)
		}
	}
}

func TestGenerateProvenance_RejectsMalformedSHA(t *testing.T) {
	t.Parallel()

	for _, sha := range []string{"", "deadbeef", strings.Repeat("A", 40), strings.Repeat("g", 64), strings.Repeat("2", 41)} {
		_, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
			Checksums:     strings.NewReader(strings.Repeat("b", 64) + "  x\n"),
			RepositoryURL: "https://git.example/o/r",
			Ref:           "v1",
			SHA:           sha,
			BuilderID:     "https://git.example/o/r/release.yml@v1",
			InvocationID:  "https://git.example/o/r/actions/runs/1",
			StartedOn:     "2026-06-01T00:00:00Z",
		})
		if !errors.Is(err, errs.ErrValidation) {
			t.Errorf("SHA %q: err = %v, want ErrValidation", sha, err)
		}
	}
}

// statement is a minimal view of the fields the tests assert on.
type statement struct {
	Predicate struct {
		BuildDefinition struct {
			BuildType            string                     `json:"buildType"`
			ExternalParameters   map[string]json.RawMessage `json:"externalParameters"`
			InternalParameters   map[string]string          `json:"internalParameters"`
			ResolvedDependencies []json.RawMessage          `json:"resolvedDependencies"`
		} `json:"buildDefinition"`
		RunDetails struct {
			Builder struct {
				ID string `json:"id"`
			} `json:"builder"`
			Metadata struct {
				InvocationID string `json:"invocationId"`
			} `json:"metadata"`
		} `json:"runDetails"`
	} `json:"predicate"`
}

func decodeStatement(t *testing.T, body []byte) statement {
	t.Helper()

	var s statement
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	return s
}

// dependency is a minimal view of one resolvedDependencies entry.
type dependency struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest"`
}

func decodeDependency(t *testing.T, raw json.RawMessage) dependency {
	t.Helper()

	var dep dependency
	if err := json.Unmarshal(raw, &dep); err != nil {
		t.Fatalf("resolved dependency is not valid JSON: %v", err)
	}

	return dep
}

func TestGenerateProvenance_NoGoSum(t *testing.T) {
	t.Parallel()

	body, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
		Checksums:     strings.NewReader(strings.Repeat("b", 64) + "  x\n"),
		GoSum:         nil, // optional
		RepositoryURL: "https://git.example/o/r",
		Ref:           "v1",
		SHA:           strings.Repeat("a", 40),
		BuilderID:     "https://git.example/o/r/release.yml@v1",
		InvocationID:  "https://git.example/o/r/actions/runs/1",
		StartedOn:     "2026-06-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	if n := len(decodeStatement(t, body).Predicate.BuildDefinition.ResolvedDependencies); n != 1 {
		t.Errorf("got %d deps, want 1 (source only when no go.sum)", n)
	}
}

// TestGenerateProvenance_ForgejoProfileNamesTheWorkflowNotAGenericSource pins
// what the forgejo-actions profile changes: the build is described as a
// workflow run — its own buildType, runner and workflow parameters — and the
// generic externalParameters.source is deliberately absent, because under this
// profile the workflow parameters carry that identity instead.
//
// What the profile must not change is the dependency list, so the count is
// asserted here. What the entries look like is the release statement's own
// behaviour and is pinned by TestGenerateProvenance_CarriesIdentifiersAndDeps.
func TestGenerateProvenance_ForgejoProfileNamesTheWorkflowNotAGenericSource(t *testing.T) {
	t.Parallel()

	body, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
		Checksums:     strings.NewReader(strings.Repeat("a", 64) + "  app.tar.gz\n"),
		GoSum:         strings.NewReader("example.com/mod v0.1.0+incompatible h1:abc123=\n"),
		RepositoryURL: "https://codeberg.org/Itiquette/example",
		Ref:           "v1.2.3",
		SHA:           strings.Repeat("2", 40),
		BuilderID:     "https://runner.example/the-generic-builder",
		InvocationID:  "https://codeberg.org/Itiquette/example/actions/runs/123",
		StartedOn:     "2026-06-01T00:00:00Z",
		Profile:       apprelease.ProvenanceProfileForgejoActions,
		Workflow:      "release.yml",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := decodeStatement(t, body)

	// The profile names its own builder, the workflow at the source ref, and
	// does not take the generic one it was handed.
	if id := got.Predicate.RunDetails.Builder.ID; id != "https://codeberg.org/Itiquette/example/.forgejo/workflows/release.yml@v1.2.3" {
		t.Errorf("builder id = %q", id)
	}

	build := got.Predicate.BuildDefinition
	if build.BuildType != "https://forgejo.org/actions/buildtypes/workflow/v1" {
		t.Errorf("buildType = %q", build.BuildType)
	}

	if build.InternalParameters["runner"] != "forgejo-actions" {
		t.Errorf("runner internal parameter = %q", build.InternalParameters["runner"])
	}

	var workflow struct {
		Ref        string `json:"ref"`
		Repository string `json:"repository"`
		Path       string `json:"path"`
	}
	if err := json.Unmarshal(build.ExternalParameters["workflow"], &workflow); err != nil {
		t.Fatalf("externalParameters.workflow: %v", err)
	}

	if workflow.Ref != "v1.2.3" || workflow.Repository != "https://codeberg.org/Itiquette/example" || workflow.Path != ".forgejo/workflows/release.yml" {
		t.Errorf("workflow external parameters = %+v", workflow)
	}

	if _, ok := build.ExternalParameters["source"]; ok {
		t.Error("forgejo-actions profile must not emit generic externalParameters.source")
	}

	// Naming the build after the workflow must not cost the attestation its
	// dependencies: the source commit and the go.sum module are still there.
	if n := len(build.ResolvedDependencies); n != 2 {
		t.Errorf("got %d deps, want source + module", n)
	}
}

// A base-image signer names its caller through the typed workflow flag, and
// the generic profile records that filename verbatim beside the source: the
// base verifiers match exactly this pair. The key stays reserved against
// declared extras, so it enters the lineage only through the flag.
func TestGenerateProvenance_GenericProfileRecordsTheCallerWorkflow(t *testing.T) {
	t.Parallel()

	input := func() apprelease.ProvenanceInput {
		return apprelease.ProvenanceInput{
			Checksums:     strings.NewReader(strings.Repeat("c", 64) + "  base\n"),
			RepositoryURL: "https://git.example/o/r",
			Ref:           "v1.2.3",
			SHA:           strings.Repeat("3", 40),
			BuilderID:     "https://git.example/o/r/release.yml@v1.2.3",
			InvocationID:  "https://git.example/o/r/actions/runs/7",
			StartedOn:     "2026-06-01T00:00:00Z",
			Profile:       apprelease.ProvenanceProfileGeneric,
			Workflow:      "release.yml",
		}
	}

	body, err := apprelease.GenerateProvenance(input())
	if err != nil {
		t.Fatal(err)
	}

	ext := decodeStatement(t, body).Predicate.BuildDefinition.ExternalParameters
	if string(ext["workflow"]) != `"release.yml"` || string(ext["source"]) != `"git+https://git.example/o/r"` {
		t.Errorf("externalParameters workflow = %s, source = %s", ext["workflow"], ext["source"])
	}

	forged := input()
	forged.ExternalParameters = map[string]any{"workflow": "other.yml"}

	if _, err := apprelease.GenerateProvenance(forged); !errors.Is(err, errs.ErrValidation) {
		t.Errorf("declared workflow extra: err = %v, want ErrValidation", err)
	}
}

func TestGenerateProvenance_ForgejoActionsProfileRequiresWorkflow(t *testing.T) {
	t.Parallel()

	_, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
		Checksums:     strings.NewReader(strings.Repeat("a", 64) + "  app.tar.gz\n"),
		RepositoryURL: "https://codeberg.org/itiquette/example",
		Ref:           "v1.2.3",
		SHA:           strings.Repeat("2", 40),
		BuilderID:     "builder",
		StartedOn:     "2026-06-01T00:00:00Z",
		Profile:       apprelease.ProvenanceProfileForgejoActions,
	})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

// TestGenerateProvenance_ExternalParameters pins the generic extras path
// on the release statement: caller-declared parameters land in
// buildDefinition.externalParameters, and any engine-computed key is
// reserved — a collision fails rather than overriding a fact.
func TestGenerateProvenance_ExternalParameters(t *testing.T) {
	t.Parallel()

	input := func(extras map[string]any) apprelease.ProvenanceInput {
		return apprelease.ProvenanceInput{
			Checksums:          strings.NewReader(strings.Repeat("a", 64) + "  app.tar.gz\n"),
			RepositoryURL:      "https://codeberg.org/itiquette/example",
			Ref:                "v1.2.3",
			SHA:                strings.Repeat("2", 40),
			BuilderID:          "https://codeberg.org/itiquette/example/release.yml@v1.2.3",
			InvocationID:       "https://codeberg.org/itiquette/example/actions/runs/1",
			StartedOn:          "2026-06-01T00:00:00Z",
			ExternalParameters: extras,
		}
	}

	t.Run("extras land in externalParameters", func(t *testing.T) {
		t.Parallel()

		body, err := apprelease.GenerateProvenance(input(map[string]any{"base_input_set": "abc123", "build_group": "core"}))
		if err != nil {
			t.Fatal(err)
		}

		ext := decodeStatement(t, body).Predicate.BuildDefinition.ExternalParameters
		if string(ext["base_input_set"]) != `"abc123"` || string(ext["build_group"]) != `"core"` {
			t.Errorf("extras missing from externalParameters: %#v", ext)
		}
	})

	// Every key the engine computed is reserved, so a declared document can
	// never shadow an engine fact. Only "source" was covered, which left the
	// rule stated once rather than for each key it protects.
	forgejo := func(extras map[string]any) apprelease.ProvenanceInput {
		in := input(extras)
		in.Profile = apprelease.ProvenanceProfileForgejoActions
		in.Workflow = "release.yml"

		return in
	}

	for name, testCase := range map[string]struct {
		build func(map[string]any) apprelease.ProvenanceInput
		key   string
	}{
		"source is computed by the default profile":   {build: input, key: "source"},
		"ref is computed by the default profile":      {build: input, key: "ref"},
		"workflow is computed by the forgejo profile": {build: forgejo, key: "workflow"},
	} {
		t.Run("refuses to let a caller shadow "+testCase.key, func(t *testing.T) {
			t.Parallel()

			_, err := apprelease.GenerateProvenance(testCase.build(map[string]any{testCase.key: "shadowed"}))
			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("%s: err = %v, want ErrValidation", name, err)
			}
		})
	}
}

func TestGenerateProvenance_CompleteStatementForEachProfile(t *testing.T) {
	t.Parallel()

	// These are author-written wire expectations, not domain Build output or a
	// projection that could silently discard subjects, timestamps or extra keys.
	const (
		wantGeneric = `{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": [
			{"name": "dist/app.tar.gz", "digest": {"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
			{"name": "dist/app-sboms.zip", "digest": {"sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
		],
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate": {
			"buildDefinition": {
				"buildType": "https://diggsweden.github.io/forge-ci/release-build/v1",
				"externalParameters": {"source": "git+https://codeberg.org/itiquette/repo", "ref": "v2.0.0"},
				"internalParameters": {},
				"resolvedDependencies": [
					{"uri": "git+https://codeberg.org/itiquette/repo@v2.0.0", "digest": {"gitCommit": "dddddddddddddddddddddddddddddddddddddddd"}},
					{"uri": "pkg:golang/github.com/foo/bar@v1.0.0", "digest": {"gomod_h1": "xyz"}}
				]
			},
			"runDetails": {
				"builder": {"id": "https://codeberg.org/itiquette/repo/.forgejo/workflows/release.yml@v2.0.0"},
				"metadata": {
					"invocationId": "https://codeberg.org/itiquette/repo/actions/runs/777",
					"startedOn": "2026-06-01T12:34:56Z",
					"finishedOn": "2026-06-01T12:34:56Z"
				}
			}
		}
	}`
		wantForgejo = `{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": [
			{"name": "dist/app.tar.gz", "digest": {"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
			{"name": "dist/app-sboms.zip", "digest": {"sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
		],
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate": {
			"buildDefinition": {
				"buildType": "https://forgejo.org/actions/buildtypes/workflow/v1",
				"externalParameters": {
					"workflow": {
						"ref": "v2.0.0",
						"repository": "https://codeberg.org/itiquette/repo",
						"path": ".forgejo/workflows/release.yml"
					}
				},
				"internalParameters": {"runner": "forgejo-actions"},
				"resolvedDependencies": [
					{"uri": "git+https://codeberg.org/itiquette/repo@v2.0.0", "digest": {"gitCommit": "dddddddddddddddddddddddddddddddddddddddd"}},
					{"uri": "pkg:golang/github.com/foo/bar@v1.0.0", "digest": {"gomod_h1": "xyz"}}
				]
			},
			"runDetails": {
				"builder": {"id": "https://codeberg.org/itiquette/repo/.forgejo/workflows/release.yml@v2.0.0"},
				"metadata": {
					"invocationId": "https://codeberg.org/itiquette/repo/actions/runs/777",
					"startedOn": "2026-06-01T12:34:56Z",
					"finishedOn": "2026-06-01T12:34:56Z"
				}
			}
		}
	}`
	)

	for _, tc := range []struct {
		name     string
		profile  apprelease.ProvenanceProfile
		workflow string
		want     string
	}{
		{name: "generic", profile: "", workflow: "", want: wantGeneric},
		{name: "forgejo workflow basename", profile: "forgejo-actions", workflow: "release.yml", want: wantForgejo},
		{name: "forgejo prefixed workflow path", profile: "forgejo-actions", workflow: ".forgejo/workflows/release.yml", want: wantForgejo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
				Checksums: strings.NewReader(
					"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  dist/app.tar.gz\n" +
						"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  dist/app-sboms.zip\n"),
				GoSum:         strings.NewReader("github.com/foo/bar v1.0.0 h1:xyz=\n"),
				RepositoryURL: "https://codeberg.org/itiquette/repo",
				Ref:           "v2.0.0",
				SHA:           "dddddddddddddddddddddddddddddddddddddddd",
				BuilderID:     "https://codeberg.org/itiquette/repo/.forgejo/workflows/release.yml@v2.0.0",
				InvocationID:  "https://codeberg.org/itiquette/repo/actions/runs/777",
				StartedOn:     "2026-06-01T12:34:56Z",
				Profile:       tc.profile,
				Workflow:      tc.workflow,
			})
			if err != nil {
				t.Fatal(err)
			}

			var got, want any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("output is not valid JSON: %v", err)
			}

			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatalf("invalid expected JSON: %v", err)
			}

			if !reflect.DeepEqual(got, want) {
				t.Errorf("statement = %s\nwant %s", body, tc.want)
			}
		})
	}
}

func TestGenerateProvenance_RejectsUnknownProfile(t *testing.T) {
	t.Parallel()

	body, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
		Checksums:     strings.NewReader("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  dist/app.tar.gz\n"),
		GoSum:         strings.NewReader("github.com/foo/bar v1.0.0 h1:xyz=\n"),
		RepositoryURL: "https://codeberg.org/itiquette/repo",
		Ref:           "v2.0.0",
		SHA:           "dddddddddddddddddddddddddddddddddddddddd",
		BuilderID:     "https://codeberg.org/itiquette/repo/.forgejo/workflows/release.yml@v2.0.0",
		InvocationID:  "https://codeberg.org/itiquette/repo/actions/runs/777",
		StartedOn:     "2026-06-01T12:34:56Z",
		Profile:       "unsupported-runner",
		Workflow:      "release.yml",
	})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), `provenance: unknown profile "unsupported-runner"`) {
		t.Fatalf("err = %v, want ErrUsage identifying the unknown profile", err)
	}

	if body != nil {
		t.Errorf("output = %q, want nil on profile refusal", body)
	}
}

// The workflow value is recorded in the statement and, for the forgejo-actions
// profile, names the builder. It is held to one rule for both profiles: a
// relative path with no parent step and no control character.
func TestGenerateProvenance_WorkflowMustBeASafeRelativePath(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, workflow string
		refused        bool
	}{
		{name: "filename", workflow: "release.yml"},
		{name: "full forgejo path", workflow: ".forgejo/workflows/release.yml"},
		{name: "nested path", workflow: "sub/dir/release.yaml"},
		{name: "dots inside a name", workflow: "release..v2.yml"},
		{name: "absolute", workflow: "/etc/release.yml", refused: true},
		{name: "parent step", workflow: "../release.yml", refused: true},
		{name: "inner parent step", workflow: ".forgejo/workflows/../../release.yml", refused: true},
		{name: "bare parent step", workflow: "..", refused: true},
		{name: "line feed", workflow: "release.yml\n", refused: true},
		{name: "carriage return", workflow: "release\r.yml", refused: true},
		{name: "tab", workflow: "release\t.yml", refused: true},
		{name: "NUL", workflow: "release.yml\x00", refused: true},
		{name: "escape", workflow: "release\x1b.yml", refused: true},
		{name: "delete", workflow: "release\x7f.yml", refused: true},
	} {
		for _, profile := range []apprelease.ProvenanceProfile{apprelease.ProvenanceProfileGeneric, apprelease.ProvenanceProfileForgejoActions} {
			t.Run(tc.name+"/"+string(profile), func(t *testing.T) {
				t.Parallel()

				body, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
					Checksums:     strings.NewReader(strings.Repeat("a", 64) + "  app.tar.gz\n"),
					RepositoryURL: "https://git.example/o/r",
					Ref:           "v1.2.3",
					SHA:           strings.Repeat("2", 40),
					BuilderID:     "https://git.example/o/r/builder",
					InvocationID:  "https://git.example/o/r/actions/runs/1",
					StartedOn:     "2026-06-01T00:00:00Z",
					Profile:       profile,
					Workflow:      tc.workflow,
				})

				if !tc.refused {
					if err != nil {
						t.Fatalf("workflow %q refused: %v", tc.workflow, err)
					}

					return
				}

				if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "--workflow must be a relative workflow path") {
					t.Errorf("workflow %q: err = %v, want ErrUsage naming --workflow", tc.workflow, err)
				}

				if body != nil {
					t.Errorf("workflow %q: statement = %q, want none on refusal", tc.workflow, body)
				}
			})
		}
	}
}
