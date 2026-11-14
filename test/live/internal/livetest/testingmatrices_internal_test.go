// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// TestTestingMatricesInSync asserts that the generated sections of
// docs/testing-matrices.md, the E2E variants and the demos, match what the
// tree says now: the shipped demos' declarations as the admission reads
// them, the case tables and the live-evidence overlay. A demo, a case or a
// mark changed without the page is a matrix that lies about coverage.
//
// On failure, run `just gen-testing-matrices` to refresh the page.
func TestTestingMatricesInSync(t *testing.T) {
	t.Parallel()

	got := reporoot.ReadFile(t, TestingMatricesPage)

	want, err := RenderTestingMatrices(reporoot.Path(t), string(got))
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != want {
		t.Fatalf("%s is stale; run just gen-testing-matrices", TestingMatricesPage)
	}
}

// The overlay is hand-kept, so a malformed line is refused with its number
// rather than silently dropped from the matrices.
func TestReadMatrixEvidenceRefusesMalformedLines(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"short":    "forgejo hello-maven-service lifecycle\n",
		"provider": "bitbucket hello-maven-service lifecycle ✅\n",
		"twice":    "forgejo hello-maven-service lifecycle ✅\nforgejo hello-maven-service lifecycle ◐ again\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := writeFile(t, filepath.Join(t.TempDir(), "evidence.txt"), "# comment\n\n"+body)
			if _, err := readMatrixEvidence(path); err == nil || !strings.Contains(err.Error(), "evidence.txt:") {
				t.Fatalf("malformed overlay: %v, want a refusal naming the line", err)
			}
		})
	}
}

// The matrices are read from the tree, so the shipped tree decides them: the
// service demos have lifecycle lines on both lab forges, the npm service is
// admitted on GitHub without a fixture for its development build, a GitHub
// line is the fixture workflow a demo ships, a refusal carries the planner's
// or the admission's reason, the harness's scope listed apart, and a GitLab
// lifecycle without the demo's release build is not applicable. The Go
// service extracts its binaries, yet its snapshot is container-first, and
// the snapshot event runs it on GitLab, where no release build is needed.
// Each one-image service ships its hosted development fixture, so its GitHub
// development and lifecycle cells have a line.
// The multi-image demos' snapshot lines judge every planned image; the
// monorepo has one on each lab forge.
func TestRenderTestingMatricesReadsTheShippedTree(t *testing.T) {
	t.Parallel()

	// The shipped overlay must load, and is then left out: these lines are
	// what the tree alone says, which no recorded run changes.
	data, err := readMatrices(reporoot.Path(t))
	if err != nil {
		t.Fatal(err)
	}

	data.evidence = nil
	page := data.projectKindsTable() + data.variantsSection()

	for _, want := range []string{
		"| hello-maven-service (java · artifact-first · one image) |",
		"| hello-cargo-container (rust · container-first · one image) |",
		"| hello-npm-service (node · artifact-first · one image) |",
		"| 📝 lifecycle / 📝 lifecycle / 📝 lifecycle |",
		"| hello-ios (xcode-ios · no container · no image) | ⬚ / ⬚ / 📝 |",
		"- hello-monorepo, github: the demo has no root Containerfile",
		"| hello-go-cli (go · no container · no image) |",
		"| hello-go-service (go · container-first · one image) |",
		"| — | 📝 lifecycle / 📝 snapshot / 📝 lifecycle |",
		"| hello-cargo-workspace (rust · container-first · 2 images) |",
		"| — | 📝 snapshot / 📝 snapshot / ⛔ |",
		"| 📝 snapshot / 📝 snapshot / ⛔ | — | 📝 / 📝 / 📝 |",
		"| 📝 / — / 📝 |",
		"Outside the live harness's scope, the trial's acceptance and not a product rule:\n\n- hello-cargo-workspace, forgejo:",
		"|  | container-first | 📝 hello-cargo-container | 📝 hello-cargo-container | 📝 hello-cargo-container |",
		"| architecture | amd64 |",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("matrices lack %q:\n%s", want, page)
		}
	}

	if _, err := RenderTestingMatrices(reporoot.Path(t), "no markers"); err == nil {
		t.Fatal("a page without the generated markers was rendered")
	}
}

// The seed keeps each flow's latest run whatever the record order; a run of
// no demo is left out. A run with a release option is another case than a
// plain run of its flow, so a later plain pass leaves its failure standing.
func TestSeedTestingEvidenceKeepsTheLatestRunOfEachFlow(t *testing.T) {
	t.Parallel()

	live := t.TempDir()
	verdict := func(run, started, result, consumer, settings string) {
		writeFile(t, filepath.Join(live, run, "verdict.json"), `{"verdict":"`+result+`","run":{"provider":"gitlab","started_at":"`+started+
			`","scenario":"^TestGitLabRelease_ConsumerTrial$","inputs":{"forge_ci_live_consumer":"`+consumer+`","forge_ci_live_consumer_settings":"`+settings+`"}}}`)
	}
	verdict("b-older", "2026-11-01T10:00:00Z", "failed", "/c/demos/hello-maven/consumer", "")
	verdict("a-newer", "2026-11-02T10:00:00+01:00", "passed", "/c/demos/hello-maven/consumer", "")
	verdict("c-no-demo", "2026-11-03T10:00:00Z", "passed", "/c/elsewhere", "")
	verdict("d-option", "2026-11-04T10:00:00Z", "failed", "/c/demos/hello-go-cli/consumer", "release-option=draft")
	verdict("e-plain", "2026-11-05T10:00:00Z", "passed", "/c/demos/hello-go-cli/consumer", "")

	seed, err := SeedTestingEvidence(live)
	if err != nil {
		t.Fatal(err)
	}

	if want := "gitlab hello-go-cli release ❌ 2026-11-04\ngitlab hello-maven release ✅ 2026-11-02\n"; seed != want {
		t.Fatalf("seed:\n%s\nwant:\n%s", seed, want)
	}
}

// An overlay mark no cell would show is refused, not dropped.
func TestReadMatricesRefusesAnUnknownSubject(t *testing.T) {
	t.Parallel()

	data := matrices{demos: []matrixDemo{{name: "hello-maven"}}, evidence: matrixEvidence{"github/hello-mavn/release": {{mark: "✅"}}}}
	if err := data.knownSubjects(); err == nil || !strings.Contains(err.Error(), "hello-mavn is no shipped demo") {
		t.Fatalf("unknown subject: %v, want a refusal naming it", err)
	}
}

// A mark qualified with its run's axis values shows on those values alone;
// an unqualified mark leaves a run-selected axis at the line, since nothing
// says which value proved it. A case's builder is its own, and the caller's
// visibility covers both.
func TestLaneValueCellShowsAMarkOnTheValuesItsRunHad(t *testing.T) {
	t.Parallel()

	lane := matrixLane{name: "lc-forgejo", provider: forgeForgejo, cases: []PreparedCase{
		{Demo: "qualified", Event: TrialLifecycle, Builder: ImageBuilderBuildah}, {Demo: "qualified", Event: TrialLifecycle, Builder: ImageBuilderKaniko},
		{Demo: "unqualified", Event: TrialLifecycle, Builder: ImageBuilderBuildah},
	}}
	evidence := matrixEvidence{
		"forgejo/qualified/lifecycle":   {{mark: "✅", note: "root", axes: map[string]string{settingVisibility: visibilityPublic, settingImageBuilder: ImageBuilderBuildah}}},
		"forgejo/unqualified/lifecycle": {{mark: "✅", note: "root", axes: map[string]string{}}},
	}

	for name, test := range map[string]struct {
		demo, setting, value, want string
	}{
		"qualified, its builder":       {"qualified", settingImageBuilder, ImageBuilderBuildah, "✅ lc-forgejo (root)"},
		"qualified, the other builder": {"qualified", settingImageBuilder, ImageBuilderKaniko, "📝 lc-forgejo"},
		"qualified, its visibility":    {"qualified", settingVisibility, visibilityPublic, "✅ lc-forgejo (root)"},
		"qualified, another":           {"qualified", settingVisibility, visibilityPrivate, "📝 lc-forgejo"},
		"qualified, no admin value":    {"qualified", "admin-lint", "false", "📝 lc-forgejo"},
		"unqualified, visibility":      {"unqualified", settingVisibility, visibilityPrivate, "📝 lc-forgejo"},
		"unqualified, default builder": {"unqualified", settingImageBuilder, ImageBuilderBuildah, "📝 lc-forgejo"},
		"unqualified, no kaniko line":  {"unqualified", settingImageBuilder, ImageBuilderKaniko, "⬚ no lane"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data := matrices{demos: []matrixDemo{{name: test.demo, admission: map[string]error{}}}, lanes: []matrixLane{lane}, evidence: evidence}
			if got := data.laneValueCell(forgeForgejo, test.setting, test.value); got != test.want {
				t.Fatalf("cell %q, want %q", got, test.want)
			}
		})
	}
}

// The axis words lead the note; a flow is marked once per set of them, and
// a qualifier outside its values is refused.
func TestReadMatrixEvidenceReadsAxisQualifiers(t *testing.T) {
	t.Parallel()

	path := writeFile(t, filepath.Join(t.TempDir(), "evidence.txt"), "forgejo d lifecycle ✅ visibility=public image-builder=kaniko root a\n"+
		"forgejo d lifecycle ◐ visibility=public image-builder=buildah root b\n")

	evidence, err := readMatrixEvidence(path)
	if err != nil || len(evidence["forgejo/d/lifecycle"]) != 2 || evidence["forgejo/d/lifecycle"][0].note != "root a" ||
		evidence["forgejo/d/lifecycle"][0].axes[settingImageBuilder] != ImageBuilderKaniko {
		t.Fatalf("qualified marks %+v %v", evidence, err)
	}

	for _, body := range []string{"forgejo d lifecycle ✅ image-builder=docker\n", "forgejo d lifecycle ✅ visibility=public visibility=private\n",
		"forgejo d lifecycle ✅ visibility=public\nforgejo d lifecycle ◐ visibility=public\n"} {
		if _, err := readMatrixEvidence(writeFile(t, filepath.Join(t.TempDir(), "evidence.txt"), body)); err == nil {
			t.Errorf("%q was read, want a refusal", body)
		}
	}
}

// A pass that needed the launcher's rerun is marked ✅+rerun. The matrices
// rank it as a pass and say in the cell what it was, before the line's own
// note; no other mark takes the suffix. The seed writes the mark for a case
// whose tests passed in its row's rerun, and for no other.
func TestMatrixEvidenceMarksAPassOnRerun(t *testing.T) {
	t.Parallel()

	path := writeFile(t, filepath.Join(t.TempDir(), "evidence.txt"), "forgejo d release ✅+rerun 2026-11-11\nforgejo d pull-request ✅+rerun\n")

	evidence, err := readMatrixEvidence(path)
	if err != nil {
		t.Fatal(err)
	}

	release, pullRequest := evidence["forgejo/d/release"][0], evidence["forgejo/d/pull-request"][0]
	if release.mark != "✅" || release.cell("") != "✅ (passed on rerun after an unavailable dependency, 2026-11-11)" ||
		pullRequest.cell("") != "✅ (passed on rerun after an unavailable dependency)" {
		t.Errorf("cells %q and %q", release.cell(""), pullRequest.cell(""))
	}

	if _, err := readMatrixEvidence(writeFile(t, filepath.Join(t.TempDir(), "evidence.txt"), "forgejo d release ❌+rerun\n")); err == nil {
		t.Error("a failure was marked as a rerun")
	}

	live := t.TempDir()
	writeFile(t, filepath.Join(live, "table", "verdict.json"), `{"verdict":"passed","run":{"provider":"forgejo","started_at":"2026-11-11T10:00:00Z"},"tests":[`+
		`{"name":"TestForgejoLayer_ConsumerTrial/release-variants/hello-go-cli/release/buildah/public","result":"pass"},`+
		`{"name":"TestForgejoLayer_ConsumerTrial/release-variants/hello-go-cli/release/buildah/private","result":"pass","attempt":2},`+
		`{"name":"TestForgejoLayer_Snapshot/snapshot/hello-cargo-workspace/snapshot/buildah/public","result":"pass"}]}`)

	seed, err := SeedTestingEvidence(live)
	if err != nil {
		t.Fatal(err)
	}

	if want := "forgejo hello-cargo-workspace development ✅ 2026-11-11\nforgejo hello-go-cli release ✅+rerun 2026-11-11\n"; seed != want {
		t.Fatalf("seed:\n%s\nwant:\n%s", seed, want)
	}

	if _, err := readMatrixEvidence(writeFile(t, filepath.Join(t.TempDir(), "evidence.txt"), seed)); err != nil {
		t.Fatalf("the overlay does not read its own seed: %v", err)
	}
}

// The latest result of each case is kept and a row marked with the worst of
// its cases, so a later run of other cases leaves a failure standing and a
// rerun of the failed case replaces it. A delta run adds to the run of its
// set after on its commit, so a failure there is not hidden by the delta's
// pass, and a run that is no delta replaces it. A delta run with no such
// run before it, on its commit from its prepared directory with its pattern
// and, for a delta in turn, its own run before it, is left out with a
// comment that names it. Runs that record no prepared directory match only
// each other.
func TestSeedTestingEvidenceAddsADeltaRunToItsRunsBefore(t *testing.T) {
	t.Parallel()

	const (
		public  = "release-variants/hello-go-cli/release/buildah/public"
		private = "release-variants/hello-go-cli/release/buildah/private"
		kaniko  = "private/hello-go-cli/release/kaniko/private"
	)

	// A run is <day>:<commit>:<set>[:<after>][:<pattern>][:<prepared.env
	// hash>][:<case>,...], runs the public release-variants case unless it
	// names its cases, fails on day 1 and is interrupted on day 4.
	write := func(live, run string) {
		fields := append(strings.Split(run, ":"), "", "", "", "")
		day, commit, set, after, pattern := fields[0], fields[1], fields[2], fields[3], cmp.Or(fields[4], "^TestForgejoLayer_")
		hash, cases := fields[5], strings.Split(cmp.Or(fields[6], public), ",")

		result, leaf := verdictPassed, leafResultPass

		switch day {
		case "1":
			result, leaf = liveVerdictFailed, leafResultFail
		case "4":
			result = verdictInterrupted
		}

		tests := make([]string, 0, len(cases))
		for _, name := range cases {
			tests = append(tests, `{"name":"TestForgejoLayer_ConsumerTrial/`+name+`","result":"`+leaf+`"}`)
		}

		writeFile(t, filepath.Join(live, day, "verdict.json"), `{"verdict":"`+result+`","run":{"provider":"forgejo","started_at":"2026-11-1`+day+
			`T10:00:00Z","source_commit":"`+commit+`","scenario":"`+pattern+`","inputs":{"forge_ci_live_set":"`+set+`","forge_ci_live_after":"`+after+
			`","forge_ci_live_prepared_env_sha256":"`+hash+`"}},"tests":[`+strings.Join(tests, ",")+`]}`)
	}

	const (
		line    = "forgejo hello-go-cli release "
		leftOut = "# left out: 2, the medium set after small, has no earlier complete run of the small set on its commit from its prepared directory with its pattern\n"
	)

	for _, test := range []struct {
		runs []string
		want string
	}{
		{runs: []string{"1:c1:small", "2:c1:medium:small"}, want: line + "❌ 2026-11-12"},
		{runs: []string{"1:c1:small", "2:c1:medium"}, want: line + "✅ 2026-11-12"},
		{runs: []string{"1:c1:small", "2:c1:medium:small", "3:c1:full:medium"}, want: line + "❌ 2026-11-13"},
		{runs: []string{"1:c1:small", "2:c2:medium:small"}, want: leftOut + line + "❌ 2026-11-11"},
		{runs: []string{"1:c1:small", "3:c1:full:medium"}, want: "# left out: 3, the full set after medium, has no earlier complete run of the medium set on its commit from its prepared directory with its pattern\n" + line + "❌ 2026-11-11"},
		{runs: []string{"1:c1:small", "2:c1:medium:small:^TestForgejo"}, want: leftOut + line + "❌ 2026-11-11"},
		// Another lane's later pass of other cases of the row hides no failure;
		// a later pass of the failed case replaces it.
		{runs: []string{"1:c1:full::^private$::" + kaniko, "2:c1:full::^release-variants$"}, want: line + "❌ 2026-11-11"},
		{runs: []string{"1:c1:full::^private$::" + kaniko, "2:c1:full::^release-variants$", "3:c1:full::^private$::" + kaniko}, want: line + "✅ 2026-11-13"},
		// A delta keeps the worse of each case it shares with its run before.
		{runs: []string{"1:c1:small::::" + public, "2:c1:medium:small:::" + public + "," + private}, want: line + "❌ 2026-11-12"},
		{runs: []string{"1:c1:small::::" + public, "2:c1:medium:small:::" + public + "," + private, "3:c1:full::::" + public}, want: line + "✅ 2026-11-13"},
		// The prepared directory must be the delta's, both recorded or neither.
		{runs: []string{"1:c1:small:::h1", "2:c1:medium:small::h1"}, want: line + "❌ 2026-11-12"},
		{runs: []string{"1:c1:small:::h1", "2:c1:medium:small::h2"}, want: leftOut + line + "❌ 2026-11-11"},
		{runs: []string{"1:c1:small", "2:c1:medium:small::h1"}, want: leftOut + line + "❌ 2026-11-11"},
		{runs: []string{"2:c1:medium:small", "3:c1:full:medium"}, want: "# left out: 2, the medium set after small, has no earlier complete run of the small set on its commit from its prepared directory with its pattern\n" +
			"# left out: 3, the full set after medium, has no earlier complete run of the medium set on its commit from its prepared directory with its pattern\n"},
		{runs: []string{"4:c1:small", "5:c1:medium:small"}, want: "# left out: 5, the medium set after small, has no earlier complete run of the small set on its commit from its prepared directory with its pattern\n" + line + "✅ 2026-11-14"},
	} {
		live := t.TempDir()
		for _, run := range test.runs {
			write(live, run)
		}

		seed, err := SeedTestingEvidence(live)
		if want := strings.TrimSuffix(test.want, "\n") + "\n"; err != nil || seed != want {
			t.Errorf("runs %q gave %q, %v; want %q", test.runs, seed, err, want)
		}

		if _, err := readMatrixEvidence(writeFile(t, filepath.Join(t.TempDir(), "evidence.txt"), seed)); err != nil {
			t.Errorf("runs %q: the overlay does not read its own seed: %v", test.runs, err)
		}
	}
}

// A lifecycle run is seeded for the axis values its verdict header records,
// so runs of two builders are two lines, each qualified as the overlay reads
// it; an axis the header lacks is left out.
func TestSeedTestingEvidenceQualifiesLifecycleRuns(t *testing.T) {
	t.Parallel()

	live := t.TempDir()
	verdict := func(run, settings, extra string) {
		writeFile(t, filepath.Join(live, run, "verdict.json"), `{"verdict":"passed","run":{"provider":"forgejo","started_at":"2026-11-08T10:00:00Z",`+
			`"scenario":"^TestForgejoLayer_ConsumerLifecycle$","inputs":{"forge_ci_live_consumer":"/c/demos/hello-maven-service/consumer",`+
			`"forge_ci_live_consumer_event":"lifecycle","forge_ci_live_consumer_settings":"`+settings+`"`+extra+`}}}`)
	}
	verdict("a", `CI_IMAGE_BUILDER=kaniko`, `,"forge_ci_live_consumer_visibility":"private","forge_ci_live_admin_lint":"false"`)
	verdict("b", `CI_JOB_IMAGE=x`, ``)

	seed, err := SeedTestingEvidence(live)
	if err != nil {
		t.Fatal(err)
	}

	if want := `forgejo hello-maven-service lifecycle ✅ visibility=both image-builder=buildah 2026-11-08
forgejo hello-maven-service lifecycle ✅ visibility=private image-builder=kaniko admin-lint=false 2026-11-08
`; seed != want {
		t.Fatalf("seed:\n%s\nwant:\n%s", seed, want)
	}

	if _, err := readMatrixEvidence(writeFile(t, filepath.Join(t.TempDir(), "evidence.txt"), seed)); err != nil {
		t.Fatalf("the overlay does not read its own seed: %v", err)
	}
}

// A run of the prepared cases names no consumer in its header: each case is
// read from its own subtests. A case with a failed test is failed whatever
// its other tests did, one still running is unfinished, a lifecycle carries
// its builder and visibility, and a test of no case is left out, as is a case
// under another test than the one that runs its forge's cases of its event.
func TestSeedTestingEvidenceReadsPreparedCasesFromTheirTests(t *testing.T) {
	t.Parallel()

	live := t.TempDir()
	writeFile(t, filepath.Join(live, "table", "verdict.json"), `{"verdict":"failed","run":{"provider":"forgejo","started_at":"2026-11-10T10:00:00Z",`+
		`"inputs":{"forge_ci_live_admin_lint":"false"}},"tests":[`+
		`{"name":"TestForgejoLayer_ConsumerLifecycle/lifecycle/hello-maven-service/lifecycle/kaniko/public/merge","result":"pass"},`+
		`{"name":"TestForgejoLayer_ConsumerLifecycle/lifecycle/hello-maven-service/lifecycle/kaniko/public/release","result":"pass"},`+
		`{"name":"TestForgejoLayer_ConsumerLifecycle/lifecycle/hello-go-service/lifecycle/buildah/private","result":"unfinished"},`+
		`{"name":"TestForgejoLayer_ConsumerTrial/release-variants/hello-go-cli/release/buildah/public","result":"pass"},`+
		`{"name":"TestForgejoLayer_ConsumerTrial/release-variants/hello-go-cli/release/buildah/private","result":"fail"},`+
		`{"name":"TestForgejoLayer_Snapshot/snapshot/hello-cargo-workspace/snapshot/buildah/public","result":"pass"},`+
		`{"name":"TestForgejoLayer_ConsumerTrial/release-variants/hello-cargo-container/release/buildah/draft/public","result":"pass"},`+
		`{"name":"TestForgejoLayer_ConsumerTrial/release-variants/hello-cargo-container/release/buildah/prerelease/private","result":"fail"},`+
		`{"name":"TestForgejoLayer_ConsumerTrial/release-variants/no-such-demo/release/buildah/public","result":"pass"},`+
		`{"name":"TestGitLabLayer_ConsumerLifecycle/lifecycle/hello-maven-service/lifecycle/kaniko/private","result":"pass"},`+
		`{"name":"TestGitLabRelease_ConsumerTrial/private/hello-maven/release/kaniko/private","result":"pass"},`+
		`{"name":"TestGitLabLayer_ConsumerTrial/private/hello-maven/release/kaniko/private","result":"pass"},`+
		`{"name":"TestForgejoLayer_ConsumerTrial/public","result":"pass"}]}`)

	seed, err := SeedTestingEvidence(live)
	if err != nil {
		t.Fatal(err)
	}

	if want := `forgejo hello-cargo-container release ❌ 2026-11-10
forgejo hello-cargo-workspace development ✅ 2026-11-10
forgejo hello-go-cli release ❌ 2026-11-10
forgejo hello-go-service lifecycle ⏳ visibility=private image-builder=buildah admin-lint=false 2026-11-10
forgejo hello-maven-service lifecycle ✅ visibility=public image-builder=kaniko admin-lint=false 2026-11-10
gitlab hello-maven release ✅ 2026-11-10
gitlab hello-maven-service lifecycle ✅ visibility=private image-builder=kaniko admin-lint=false 2026-11-10
`; seed != want {
		t.Fatalf("seed:\n%s\nwant:\n%s", seed, want)
	}

	if _, err := readMatrixEvidence(writeFile(t, filepath.Join(t.TempDir(), "evidence.txt"), seed)); err != nil {
		t.Fatalf("the overlay does not read its own seed: %v", err)
	}
}

// A snapshot run is evidence for the matrices' snapshot flow, development,
// on either lab forge, whatever event name it was started with.
func TestSeedTestingEvidenceReadsSnapshotRunsAsDevelopment(t *testing.T) {
	t.Parallel()

	live := t.TempDir()
	for provider, scenario := range map[string]string{forgeForgejo: "TestForgejoLayer_Snapshot", forgeGitLab: "TestGitLabLayer_Snapshot"} {
		writeFile(t, filepath.Join(live, provider, "verdict.json"), `{"verdict":"passed","run":{"provider":"`+provider+`","started_at":"2026-11-09T10:00:00Z",`+
			`"scenario":"^`+scenario+`$","inputs":{"forge_ci_live_consumer":"/c/demos/hello-cargo-workspace/consumer","forge_ci_live_consumer_event":"snapshot"}}}`)
	}

	seed, err := SeedTestingEvidence(live)
	if err != nil {
		t.Fatal(err)
	}

	if want := "forgejo hello-cargo-workspace development ✅ 2026-11-09\ngitlab hello-cargo-workspace development ✅ 2026-11-09\n"; seed != want {
		t.Fatalf("seed:\n%s\nwant:\n%s", seed, want)
	}
}
