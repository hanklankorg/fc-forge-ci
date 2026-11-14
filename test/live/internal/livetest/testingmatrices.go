// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
)

// docs/testing-matrices.md keeps the testing matrices. Matrix 1, project
// kind × flow, and matrix 2, the lifecycle's variant axes, are generated here
// between the page's markers from one source, what the tree says: the shipped
// demos' declarations as the planner and the demo admission read them,
// the lanes with their cases and settings (PreparedLanes), and the live marks of
// TestingMatricesEvidence, a hand-kept overlay keyed by provider, demo and
// flow, each mark qualified with the axis values its run had, since whether a
// run proved something is a judgement; SeedTestingEvidence drafts it from
// recorded verdicts. test/syncguard compares the committed page with a fresh
// rendering; `just gen-testing-matrices` refreshes it.

// TestingMatricesPage and TestingMatricesEvidence are the page and the
// overlay, relative to the repository root.
const (
	TestingMatricesPage     = "docs/testing-matrices.md"
	TestingMatricesEvidence = "test/live/evidence.txt"
)

const (
	matrixGitHub        = "github"
	matrixDevelopment   = "development"
	matrixScenario      = "scenario"
	matrixGPGConfig     = ".forge-ci/artifacts-gpg.yml"
	matrixEvidenceCols  = 4
	routeNoContainer    = "no container"
	routeRefused        = "refused snapshot"
	routeArtifactFirst  = "artifact-first"
	routeContainerFirst = "container-first"
)

// The overlay's mark of a pass, the suffix it takes for a row that passed
// on the launcher's one rerun, and what a cell says of such a pass.
const (
	evidencePassed      = "✅"
	evidenceRerunSuffix = "+rerun"
	evidenceRerunNote   = "passed on rerun after an unavailable dependency"
)

// The marks the generator writes; the overlay's marks are the legend's.
const (
	markRefused   = "⛔"
	markLine      = "📝"
	markNoLine    = "⬚"
	markNotApply  = "—"
	matrixArchRow = "amd64"
)

var errTestingMatrices = errors.New("testing matrices")

// matrixProviders are the columns. GitHub has no prepared lane: its line for
// an event is the demo's fixture workflow the hosted harness runs for it.
var matrixProviders = []string{forgeForgejo, forgeGitLab, matrixGitHub}

// githubDemoWorkflows is where a demo keeps its GitHub workflows, which a
// GitHub trial renders into .github/workflows.
const githubDemoWorkflows = "test/live/github/workflows"

// githubEvents are the hosted harness's events with a fixture workflow each;
// its lifecycle runs all three.
var githubEvents = []string{TrialPullRequest, TrialDevRelease, TrialRelease}

// matrixDemo is a shipped demo as the matrices read it.
type matrixDemo struct {
	name, source, ecosystem, route, images, signing string
	hosted                                          map[string]bool
	release                                         map[string]bool
	admission                                       map[string]error
	// snapshot is the admission of the snapshot event on each lab
	// forge, admitSnapshot's.
	snapshot map[string]error
	// snapshotRefusal is the planner's refusal of the declaration's snapshot.
	snapshotRefusal error
}

// matrixLane is one lane (PreparedLanes): its cases of the forge's table and
// its scenarios.
type matrixLane struct {
	name, provider string
	cases          []PreparedCase
	scenarios      []string
}

// matrixMark is one overlay line's mark, the axis values its run had and its
// note.
type matrixMark struct {
	mark, note string
	axes       map[string]string
}

// matrixEvidence maps provider/demo/event to the overlay's marks of it, one
// a line, each for the axis values its run had.
type matrixEvidence map[string][]matrixMark

// The settings an overlay line may qualify a mark with: the run's visibility
// (public, private or both), image builder and administrator inputs
// (admin-<check>).
const (
	settingVisibility   = "visibility"
	settingImageBuilder = "image-builder"
	visibilityBoth      = "both"
)

// adminSetting is a check's administrator setting as the testing matrices
// and the evidence overlay name it, admin-<check>.
func adminSetting(check enforcement.Check) string { return "admin-" + string(check) }

// axisValueCovers reports whether a run that had the setting at had ran it
// at value: the same value, or both visibilities, which runs each.
func axisValueCovers(setting, had, value string) bool {
	return had == value || (setting == settingVisibility && had == visibilityBoth)
}

// matrixAxisValues are the values each qualifying setting takes.
var matrixAxisValues = func() map[string][]string {
	axes := map[string][]string{
		settingVisibility:   {visibilityPublic, visibilityPrivate, visibilityBoth},
		settingImageBuilder: {ImageBuilderBuildah, ImageBuilderKaniko},
	}
	for _, check := range enforcement.Checks {
		axes[adminSetting(check)] = []string{strconv.FormatBool(true), strconv.FormatBool(false)}
	}

	return axes
}()

type matrices struct {
	demos    []matrixDemo
	lanes    []matrixLane
	evidence matrixEvidence
}

// RenderTestingMatrices returns page with its generated blocks regenerated
// from the tree under root.
func RenderTestingMatrices(root, page string) (string, error) {
	data, err := readMatrices(root)
	if err != nil {
		return "", err
	}

	page, err = replaceGeneratedBlock(page, "project-kinds", data.projectKindsTable())
	if err != nil {
		return "", err
	}

	return replaceGeneratedBlock(page, "e2e-variants", data.variantsSection())
}

func replaceGeneratedBlock(page, name, body string) (string, error) {
	start, end := "<!-- generated:"+name+" -->", "<!-- /generated:"+name+" -->"

	head, rest, found := strings.Cut(page, start)
	if !found {
		return "", fmt.Errorf("%w: %s has no %s", errTestingMatrices, TestingMatricesPage, start)
	}

	_, tail, found := strings.Cut(rest, end)
	if !found {
		return "", fmt.Errorf("%w: %s has no %s", errTestingMatrices, TestingMatricesPage, end)
	}

	return head + start + "\n\n" + body + "\n" + end + tail, nil
}

func readMatrices(root string) (matrices, error) {
	demos, err := readMatrixDemos(root)
	if err != nil {
		return matrices{}, err
	}

	evidence, err := readMatrixEvidence(filepath.Join(root, TestingMatricesEvidence))
	if err != nil {
		return matrices{}, err
	}

	data := matrices{demos: demos, lanes: matrixLanes(), evidence: evidence}

	return data, data.knownSubjects()
}

// knownSubjects refuses an overlay mark of a demo that is not shipped or of a
// scenario no lane runs, which no cell would show.
func (data matrices) knownSubjects() error {
	known := map[string]bool{}
	for _, demo := range data.demos {
		known[demo.name] = true
	}

	for _, lane := range data.lanes {
		for _, scenario := range lane.scenarios {
			known[strings.TrimSuffix(strings.TrimPrefix(scenario, "^"), "$")] = true
		}
	}

	for _, key := range slices.Sorted(maps.Keys(data.evidence)) {
		if subject := strings.Split(key, "/")[1]; !known[subject] {
			return fmt.Errorf("%w: %s: %s is no shipped demo and no lane's scenario", errTestingMatrices, TestingMatricesEvidence, subject)
		}
	}

	return nil
}

func readMatrixDemos(root string) ([]matrixDemo, error) {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(demosDir)))
	if err != nil {
		return nil, err
	}

	demos := []matrixDemo{}

	for _, entry := range entries {
		consumer := demoConsumerDir(root, entry.Name())
		if !entry.IsDir() || !isDir(consumer) {
			continue
		}

		demo, err := readMatrixDemo(entry.Name(), consumer)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}

		demos = append(demos, demo)
	}

	return demos, nil
}

func readMatrixDemo(name, consumer string) (matrixDemo, error) {
	demo := matrixDemo{name: name, hosted: map[string]bool{}, release: map[string]bool{}, admission: map[string]error{}, snapshot: map[string]error{}}

	source, err := ServiceSnapshotSource(consumer)
	if err != nil {
		return demo, err
	}

	demo.source = source

	for provider, file := range map[string]string{forgeForgejo: forgejoReleaseWorkflow, forgeGitLab: GitLabConsumerBuildScript} {
		if demo.release[provider], err = exists(filepath.Join(consumer, filepath.FromSlash(file))); err != nil {
			return demo, err
		}

		demo.admission[provider] = matrixLifecycleAdmission(provider, consumer)
		demo.snapshot[provider] = admitSnapshot(provider, consumer, "")
	}

	cfg, err := readMatrixDeclaration(filepath.Join(consumer, filepath.FromSlash(source)))
	if err != nil {
		return demo, err
	}

	// The artifacts, and so the ecosystem, are the default declaration's even
	// where the images are declared in a dedicated file.
	artifacts := cfg
	if source != DefaultArtifactsConfig {
		if artifacts, err = readMatrixDeclaration(filepath.Join(consumer, filepath.FromSlash(DefaultArtifactsConfig))); err != nil {
			return demo, err
		}
	}

	gpg, err := exists(filepath.Join(consumer, filepath.FromSlash(matrixGPGConfig)))
	if err != nil {
		return demo, err
	}

	if err := demo.readHosted(consumer, source); err != nil {
		return demo, err
	}

	return demo, demo.describeDeclaration(cfg, artifacts, gpg)
}

func readMatrixDeclaration(path string) (*config.Config, error) {
	body, err := os.ReadFile(path) //nolint:gosec // G304: a shipped demo's declaration.
	if err != nil {
		return nil, err
	}

	return parseServiceDeclaration(string(body))
}

// readHosted reads the demo's flows on hosted GitHub. An event has a line
// where the demo ships the fixture workflow the hosted settings name for it;
// the lifecycle has one where all three are shipped and the hosted settings
// accept the event. Its admission is the service snapshot every forge's
// lifecycle shares.
func (demo *matrixDemo) readHosted(consumer, source string) error {
	lifecycle := true

	for _, event := range githubEvents {
		settings, err := ParseGitHubTrialSettings(event, nil)
		if err != nil {
			return err
		}

		if demo.hosted[event], err = exists(filepath.Join(consumer, filepath.FromSlash(githubDemoWorkflows), settings.Workflow)); err != nil {
			return err
		}

		lifecycle = lifecycle && demo.hosted[event]
	}

	_, err := ParseGitHubTrialSettings(TrialLifecycle, nil)
	demo.hosted[TrialLifecycle] = lifecycle && err == nil
	demo.release[matrixGitHub] = demo.hosted[TrialRelease]

	demo.admission[matrixGitHub] = serviceSnapshotAdmission(consumer, source)

	return nil
}

// matrixLifecycleAdmission is the admission of a lifecycle of the
// demo on the provider, the GitLab release build included, as the lane plans
// it.
func matrixLifecycleAdmission(provider, consumer string) error {
	if err := admitLifecycle(provider, consumer); err != nil {
		return err
	}

	if provider == forgeGitLab {
		return requireGitLabReleaseBuild(consumer)
	}

	return nil
}

// describeDeclaration reads the axis values the declarations carry: the
// images' from the snapshot source, the ecosystem from the artifacts.
func (demo *matrixDemo) describeDeclaration(cfg, artifacts *config.Config, gpg bool) error {
	if err := demo.readRoute(cfg); err != nil {
		return err
	}

	typed := pipeline.NewConfigPlan(cfg)
	demo.ecosystem = matrixEcosystem(pipeline.NewConfigPlan(artifacts).Artifacts.All)
	demo.images = matrixImages(typed)
	demo.signing = cmp.Or(string(cfg.Sign.Method), "default")

	if cfg.Sign.FilesMethod != "" {
		demo.signing += ", files " + string(cfg.Sign.FilesMethod)
	}

	if gpg {
		demo.signing += ", GPG declaration"
	}

	return nil
}

func matrixEcosystem(artifacts []pipeline.PlannedArtifact) string {
	families := make([]string, 0, len(artifacts))

	for _, artifact := range artifacts {
		family, err := snapshotimage.ToolchainFamily(artifact.ProjectType)
		if err != nil {
			family = string(artifact.ProjectType)
		}

		families = append(families, family)
	}

	slices.Sort(families)

	return cmp.Or(strings.Join(slices.Compact(families), ", "), "container only")
}

// readRoute reads the snapshot route the planner takes for the declaration's
// snapshot view: artifact-first when it plans producers for the images,
// container-first when the images build alone. A snapshot the planner
// refuses takes no route and keeps the planner's refusal.
func (demo *matrixDemo) readRoute(cfg *config.Config) error {
	if len(cfg.Containers) == 0 {
		demo.route = routeNoContainer

		return nil
	}

	in, err := serviceSnapshotStandInInput()
	if err != nil {
		return err
	}

	plan, refusal := snapshotimage.New(pipeline.NewConfigPlan(cfg), in)

	switch {
	case refusal != nil:
		demo.route, demo.snapshotRefusal = routeRefused, refusal
	case len(plan.Artifacts) > 0:
		demo.route = routeArtifactFirst
	default:
		demo.route = routeContainerFirst
	}

	return nil
}

func matrixImages(typed pipeline.ConfigPlan) string {
	for _, artifact := range typed.Artifacts.All {
		if artifact.WorkingDirectory != "." {
			return "nested artifacts (monorepo)"
		}
	}

	switch len(typed.Containers.All) {
	case 0:
		return "no image"
	case 1:
		return "one image"
	default:
		return fmt.Sprintf("%d images", len(typed.Containers.All))
	}
}

// matrixLanes are the lanes as the matrices read them, in PreparedLanes'
// order.
func matrixLanes() []matrixLane {
	lanes := make([]matrixLane, 0, len(PreparedLanes))

	for _, prepared := range PreparedLanes {
		lane := matrixLane{name: prepared.Name, provider: prepared.Forge, scenarios: prepared.Scenarios}

		for _, c := range PreparedCases(prepared.Forge) {
			if prepared.Lane != "" && c.Lane == prepared.Lane {
				lane.cases = append(lane.cases, c)
			}
		}

		lanes = append(lanes, lane)
	}

	return lanes
}

// readMatrixEvidence reads the overlay: `<provider> <demo|scenario> <event>
// <mark> [<setting>=<value> ...] [note]` a line, `#` comments and blank lines
// skipped. The leading setting=value words are the axis values the marked
// run had, as its run root records them (matrixAxisValues); a subject's flow
// is marked once for each set of them. The mark ✅+rerun is ✅ for a run
// whose row passed on its rerun, and the cell says so before the note.
func readMatrixEvidence(path string) (matrixEvidence, error) {
	evidence := matrixEvidence{}

	err := eachLine(path, func(number int, line string) error {
		words := strings.Fields(line)
		if len(words) == 0 || strings.HasPrefix(words[0], "#") {
			return nil
		}

		if len(words) < matrixEvidenceCols || !slices.Contains(matrixProviders, words[0]) {
			return fmt.Errorf("%w: %s:%d: want <forgejo|gitlab|github> <demo> <event> <mark> [<setting>=<value> ...] [note]", errTestingMatrices, path, number)
		}

		mark := matrixMark{mark: words[3], axes: map[string]string{}}

		passed, rerun := strings.CutSuffix(mark.mark, evidenceRerunSuffix)
		if rerun && passed != evidencePassed {
			return fmt.Errorf("%w: %s:%d: only %s takes %s", errTestingMatrices, path, number, evidencePassed, evidenceRerunSuffix)
		}

		mark.mark = passed

		rest := words[4:]
		for len(rest) > 0 {
			setting, value, qualifies := strings.Cut(rest[0], "=")
			if values, known := matrixAxisValues[setting]; !qualifies || !known {
				break
			} else if !slices.Contains(values, value) || mark.axes[setting] != "" {
				return fmt.Errorf("%w: %s:%d: %s must be one of %s, given once", errTestingMatrices, path, number, setting, strings.Join(values, ", "))
			}

			mark.axes[setting], rest = value, rest[1:]
		}

		mark.note = strings.Join(rest, " ")
		if rerun {
			mark.note = strings.TrimSuffix(evidenceRerunNote+", "+mark.note, ", ")
		}

		key := strings.Join(words[:3], "/")
		if slices.ContainsFunc(evidence[key], func(other matrixMark) bool { return maps.Equal(other.axes, mark.axes) }) {
			return fmt.Errorf("%w: %s:%d: %s is marked twice", errTestingMatrices, path, number, key)
		}

		evidence[key] = append(evidence[key], mark)

		return nil
	})

	return evidence, err
}

// mark is the best of the overlay's marks of a subject's flow, whatever the
// axis values of their runs.
func (data matrices) mark(provider, subject, event string) (matrixMark, bool) {
	return data.axisMark(provider, subject, event, "", "")
}

// axisMark is the best of the overlay's marks of a subject's flow whose run
// had the setting at value, a run of both visibilities counting for each;
// an empty setting takes every mark.
func (data matrices) axisMark(provider, subject, event, setting, value string) (matrixMark, bool) {
	best, found := matrixMark{}, false

	for _, mark := range data.evidence[provider+"/"+subject+"/"+event] {
		if setting != "" && !axisValueCovers(setting, mark.axes[setting], value) {
			continue
		}

		if !found || markRank(mark.mark) < markRank(best.mark) {
			best, found = mark, true
		}
	}

	return best, found
}

// cell writes a state with its subject and the overlay's note, if any.
func (mark matrixMark) cell(subject string) string {
	text := strings.TrimSpace(mark.mark + " " + subject)
	if mark.note != "" {
		text += " (" + mark.note + ")"
	}

	return text
}

// hasLine reports whether a lane of the provider runs the demo's event; on
// GitHub, whether the hosted harness runs it from the demo's own workflows.
func (data matrices) hasLine(provider string, demo matrixDemo, event string) bool {
	if provider == matrixGitHub {
		return demo.hosted[event]
	}

	for _, lane := range data.lanes {
		if lane.provider == provider && len(lane.lines(demo.name, event)) > 0 {
			return true
		}
	}

	return false
}

// lines are the lane's cases of the demo's event.
func (lane matrixLane) lines(demo, event string) []PreparedCase {
	var lines []PreparedCase

	for _, c := range lane.cases {
		if c.Demo == demo && c.Event == event {
			lines = append(lines, c)
		}
	}

	return lines
}

// caseRuns reports whether a case runs with the setting at value: the
// builder is the case's own, and the visibility and the administrator inputs
// are the caller's, which take every value.
func caseRuns(setting, value string, c PreparedCase) bool {
	return setting != settingImageBuilder || c.Builder == value
}

// lifecycleApplies is whether the demo has a lifecycle on the provider: on
// GitLab it needs the demo's release build, whose commands are the
// adopter's; without one the lifecycle is not applicable, as the release.
func lifecycleApplies(provider string, demo matrixDemo) bool {
	return provider != forgeGitLab || demo.release[provider]
}

// lifecycleState is a demo's lifecycle state on a provider: not applicable,
// refused, the overlay's mark, a line that exists, or admitted without a line.
func (data matrices) lifecycleState(provider string, demo matrixDemo) matrixMark {
	if !lifecycleApplies(provider, demo) {
		return matrixMark{mark: markNotApply}
	}

	if demo.admission[provider] != nil {
		return matrixMark{mark: markRefused}
	}

	if mark, found := data.mark(provider, demo.name, TrialLifecycle); found {
		return mark
	}

	if data.hasLine(provider, demo, TrialLifecycle) {
		return matrixMark{mark: markLine}
	}

	return matrixMark{mark: markNoLine, note: "admitted, no line"}
}

func (data matrices) releaseCell(provider string, demo matrixDemo) string {
	if !demo.release[provider] {
		return markNotApply
	}

	if mark, found := data.mark(provider, demo.name, TrialRelease); found {
		return mark.cell("")
	}

	if data.hasLine(provider, demo, TrialRelease) {
		return markLine
	}

	return markNoLine
}

// developmentCell is a demo's snapshot state on a provider: the snapshot
// runs as the lifecycle's development phase where the lifecycle applies
// (lifecycleApplies) and admits the demo, and alone as the snapshot event
// where a lab forge admits that. It is not applicable where
// neither applies, the admissions' refusal where neither admits (the reasons
// are footnoted, and the overlay's note kept, as when a fixture of the demo
// ran), the overlay's mark, a lifecycle or snapshot line that would run it,
// or admitted without one.
func (data matrices) developmentCell(provider string, demo matrixDemo) string {
	lifecycle := lifecycleApplies(provider, demo) && demo.admission[provider] == nil
	snapshot := provider != matrixGitHub && demo.snapshot[provider] == nil

	if !lifecycleApplies(provider, demo) && !snapshot {
		return markNotApply
	}

	mark, found := data.mark(provider, demo.name, matrixDevelopment)

	switch {
	case !lifecycle && !snapshot:
		return matrixMark{mark: markRefused, note: mark.note}.cell("")
	case found:
		return mark.cell("")
	case lifecycle && data.hasLine(provider, demo, TrialLifecycle):
		return markLine + " lifecycle"
	case snapshot && data.hasLine(provider, demo, TrialSnapshot):
		return markLine + " snapshot"
	case data.hasLine(provider, demo, TrialDevRelease):
		return markLine
	}

	return "admitted"
}

// gateCell is a demo's pull-request gate state on a provider.
func (data matrices) gateCell(provider string, demo matrixDemo) string {
	if mark, found := data.mark(provider, demo.name, TrialPullRequest); found {
		return mark.cell("")
	}

	if data.hasLine(provider, demo, TrialPullRequest) || data.hasLine(provider, demo, TrialLifecycle) {
		return markLine
	}

	return markNoLine
}

// matrixReason is the admission's refusal without the validation sentinel.
func matrixReason(err error) string {
	reason := strings.TrimSuffix(err.Error(), ": "+errs.ErrValidation.Error())

	return strings.TrimPrefix(strings.TrimPrefix(reason, TrialLifecycle+": "), TrialSnapshot+": ")
}

// markRank orders states, the best first: the overlay's live marks, then a
// line, then admitted without a line, then refused, then not applicable, then
// nothing.
func markRank(mark string) int {
	return slices.Index([]string{"✅", "◐", "⏳", "❌", markLine, markNoLine, markRefused, markNotApply, ""}, mark)
}

// bestOf is the best lifecycle state among the demos on the provider, with
// the demo it is of; no demo carrying the value is a gap of the demos.
func (data matrices) bestOf(provider string, demos []matrixDemo) string {
	if len(demos) == 0 {
		return markNoLine + " no demo"
	}

	// Of demos in the same state, the one whose lifecycle applies on more
	// forges stands for the value: it shows the same thing in every column.
	forges := func(demo matrixDemo) int {
		count := 0

		for _, each := range matrixProviders {
			if lifecycleApplies(each, demo) {
				count++
			}
		}

		return count
	}

	best, subject, reach := matrixMark{}, "", -1

	for _, demo := range demos {
		state := data.lifecycleState(provider, demo)
		if rank := markRank(state.mark); rank < markRank(best.mark) || (rank == markRank(best.mark) && forges(demo) > reach) {
			best, subject, reach = state, demo.name, forges(demo)
		}
	}

	return best.cell(subject)
}

// axisState is a demo's lifecycle state on a provider for one axis value:
// not applicable, refused, the overlay's mark of a run that had the value, or
// the line.
func (data matrices) axisState(provider string, demo matrixDemo, setting, value string) matrixMark {
	if !lifecycleApplies(provider, demo) {
		return matrixMark{mark: markNotApply}
	}

	if demo.admission[provider] != nil {
		return matrixMark{mark: markRefused}
	}

	if mark, found := data.axisMark(provider, demo.name, TrialLifecycle, setting, value); found {
		return mark
	}

	return matrixMark{mark: markLine}
}

// laneValueCell is the best lifecycle state among the provider's lifecycle
// lines that run with the setting at value (caseRuns): the overlay's
// mark of a run that had that value, or else the line. On hosted GitHub the
// lifecycle's visibility is the run's selector, as on the lab forges; it has
// no lane for the other settings.
func (data matrices) laneValueCell(provider, setting, value string) string {
	best, subject := matrixMark{}, ""

	consider := func(demo matrixDemo, name string) {
		if state := data.axisState(provider, demo, setting, value); markRank(state.mark) < markRank(best.mark) {
			best, subject = state, name
		}
	}

	for _, demo := range data.demos {
		if provider == matrixGitHub && setting == settingVisibility && demo.hosted[TrialLifecycle] {
			consider(demo, demo.name)
		}

		for _, lane := range data.lanes {
			if lane.provider == provider && slices.ContainsFunc(lane.lines(demo.name, TrialLifecycle), func(c PreparedCase) bool { return caseRuns(setting, value, c) }) {
				consider(demo, lane.name)
			}
		}
	}

	if subject == "" {
		return markNoLine + " no lane"
	}

	return best.cell(subject)
}

type matrixRow struct {
	axis, value string
	cells       []string
}

func (data matrices) declaredRows(axis string, value func(matrixDemo) string) []matrixRow {
	byValue := map[string][]matrixDemo{}

	for _, demo := range data.demos {
		for v := range strings.SplitSeq(value(demo), ", ") {
			byValue[v] = append(byValue[v], demo)
		}
	}

	rows := make([]matrixRow, 0, len(byValue))

	for _, v := range slices.Sorted(maps.Keys(byValue)) {
		row := matrixRow{axis: axis, value: v}
		for _, provider := range matrixProviders {
			row.cells = append(row.cells, data.bestOf(provider, byValue[v]))
		}

		rows = append(rows, row)
	}

	return rows
}

func (data matrices) laneRows(axis, setting string, values []string) []matrixRow {
	rows := make([]matrixRow, 0, len(values))

	for _, value := range values {
		row := matrixRow{axis: axis, value: value}
		for _, provider := range matrixProviders {
			row.cells = append(row.cells, data.laneValueCell(provider, setting, value))
		}

		rows = append(rows, row)
	}

	return rows
}

// enforcementRows are one row per administrator check enforced by a lane,
// and one for none.
func (data matrices) enforcementRows() []matrixRow {
	rows := []matrixRow{}

	for _, check := range enforcement.Checks {
		rows = append(rows, data.laneRows("enforcement", "admin-"+string(check), []string{strconv.FormatBool(true)})[0])
		rows[len(rows)-1].value = "admin " + string(check)
	}

	none := data.laneRows("enforcement", "admin-"+string(enforcement.Checks[0]), []string{strconv.FormatBool(false)})[0]
	none.value = "none enforced"

	return append(rows, none)
}

func (data matrices) architectureRows() []matrixRow {
	amd64 := matrixRow{axis: "architecture", value: matrixArchRow}
	for _, provider := range matrixProviders {
		amd64.cells = append(amd64.cells, data.bestOf(provider, data.demos))
	}

	arm64 := matrixRow{axis: "architecture", value: "arm64"}
	for range matrixProviders {
		arm64.cells = append(arm64.cells, markNotApply+" D-5")
	}

	return []matrixRow{amd64, arm64}
}

// variantsSection is matrix 2: the lifecycle event over every axis value the
// demos and the lanes carry, then the kept scenario lines.
func (data matrices) variantsSection() string {
	var page strings.Builder

	page.WriteString("| Axis | Variant | Forgejo | GitLab | GitHub |\n|---|---|---|---|---|\n")

	rows := slices.Concat(
		data.declaredRows("ecosystem", func(demo matrixDemo) string { return demo.ecosystem }),
		data.declaredRows("route", func(demo matrixDemo) string { return demo.route }),
		data.declaredRows("images", func(demo matrixDemo) string { return demo.images }),
		data.declaredRows("signing", func(demo matrixDemo) string { return demo.signing }),
		data.laneRows("image builder", settingImageBuilder, []string{ImageBuilderBuildah, ImageBuilderKaniko}),
		data.laneRows("visibility", settingVisibility, []string{visibilityPublic, visibilityPrivate}),
		data.enforcementRows(),
		data.architectureRows(),
	)

	axis := ""

	for _, row := range rows {
		shown := row.axis
		if shown == axis {
			shown = ""
		}

		axis = row.axis

		fmt.Fprintf(&page, "| %s | %s | %s |\n", shown, row.value, strings.Join(row.cells, " | "))
	}

	page.WriteString("\nKept scenario lines, by lane:\n\n| Lane | Scenario | State |\n|---|---|---|\n")

	for _, lane := range data.lanes {
		for _, scenario := range lane.scenarios {
			mark, found := data.mark(lane.provider, scenario, matrixScenario)
			if !found {
				mark = matrixMark{mark: markLine}
			}

			fmt.Fprintf(&page, "| %s | `%s` | %s |\n", lane.name, scenario, mark.cell(""))
		}
	}

	return page.String()
}

// projectKindsTable is matrix 1: every shipped demo as the project kind it
// embodies, read from its declaration, against the flows the orchestrators
// offer, a cell per forge. The snapshot column is the one the demo's route
// takes; the other is not applicable. The refusals' reasons follow the table,
// the planner's and the admission's apart from the live harness's scope.
func (data matrices) projectKindsTable() string {
	var page strings.Builder

	page.WriteString("| Demo (project kind) | PR / MR gate | Artifact-first snapshot | Container-first snapshot | Release | Full lifecycle |\n|---|---|---|---|---|---|\n")

	for _, demo := range data.demos {
		artifact, container := data.snapshotCells(demo)
		fmt.Fprintf(&page, "| %s (%s · %s · %s) | %s | %s | %s | %s | %s |\n", demo.name, demo.ecosystem, demo.route, demo.images,
			perForge(demo, data.gateCell), artifact, container,
			perForge(demo, data.releaseCell),
			perForge(demo, func(provider string, demo matrixDemo) string { return data.lifecycleState(provider, demo).cell("") }))
	}

	refused, scope := data.refusalNotes()

	for _, group := range []struct {
		heading string
		lines   []string
	}{
		{"Refused snapshots and lifecycles, with the planner's or the admission's reason:", refused},
		{"Outside the live harness's scope, the trial's acceptance and not a product rule:", scope},
	} {
		if len(group.lines) > 0 {
			page.WriteString("\n" + group.heading + "\n\n" + strings.Join(group.lines, ""))
		}
	}

	return page.String()
}

// refusalNotes are the reasons behind matrix 1's refusals: the planner's and
// the admissions', and apart from them the live harness's scope. A
// lifecycle's are given where the lifecycle applies, the snapshot event's on
// the lab forges where the planner's do not already give them.
func (data matrices) refusalNotes() ([]string, []string) {
	var refused, scope []string

	note := func(err error, subject string) {
		line := fmt.Sprintf("- %s: %s\n", subject, matrixReason(err))
		if harnessScope(err) {
			scope = append(scope, line)
		} else {
			refused = append(refused, line)
		}
	}

	for _, demo := range data.demos {
		if demo.snapshotRefusal != nil {
			refused = append(refused, fmt.Sprintf("- %s, every forge: the planner refuses its snapshot: %s\n", demo.name, demo.snapshotRefusal))
		}

		if demo.route == routeNoContainer {
			continue
		}

		for _, provider := range matrixProviders {
			if err := demo.admission[provider]; err != nil && lifecycleApplies(provider, demo) {
				note(err, demo.name+", "+provider)
			}
		}

		for _, provider := range []string{forgeForgejo, forgeGitLab} {
			if err := demo.snapshot[provider]; err != nil && demo.snapshotRefusal == nil {
				note(err, demo.name+", "+provider+" "+TrialSnapshot)
			}
		}
	}

	return refused, scope
}

// harnessScope is whether a lifecycle or snapshot refusal is the live
// harness's scope, a demo the service development trial does not judge
// (serviceSnapshotScope, snapshotImagesAdmitted and a demo without the files
// the route is written from), rather than the planner's.
func harnessScope(err error) bool {
	return errors.Is(err, errServiceDeclaration) || errors.Is(err, errSnapshotImages) || errors.Is(err, errNoDevelopmentRoute)
}

// perForge writes a flow's Forgejo, GitLab and GitHub cells.
func perForge(demo matrixDemo, cell func(string, matrixDemo) string) string {
	cells := make([]string, 0, len(matrixProviders))
	for _, provider := range matrixProviders {
		cells = append(cells, cell(provider, demo))
	}

	return strings.Join(cells, " / ")
}

// snapshotCells are the artifact-first and container-first snapshot cells:
// the demo's route decides which applies, and a demo without a container has
// neither.
func (data matrices) snapshotCells(demo matrixDemo) (string, string) {
	if demo.route == routeNoContainer {
		return markNotApply, markNotApply
	}

	state := perForge(demo, data.developmentCell)

	switch demo.route {
	case routeArtifactFirst:
		return state, markNotApply
	case routeContainerFirst:
		return markNotApply, state
	default:
		return state, state
	}
}
