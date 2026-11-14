// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package cli assembles the top-level urfave/cli v3 command tree and
// related CLI-facing helpers.
//
// The package wires global flags, subgroups, and docs rendering. It does not
// implement any business logic — that lives under internal/app/.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli/cmdmeta"
	cmdartifact "github.com/diggsweden/forge-ci/internal/cli/commands/artifact"
	cmdbuild "github.com/diggsweden/forge-ci/internal/cli/commands/build"
	cmdconfig "github.com/diggsweden/forge-ci/internal/cli/commands/config"
	cmdcontainer "github.com/diggsweden/forge-ci/internal/cli/commands/container"
	cmddocs "github.com/diggsweden/forge-ci/internal/cli/commands/docs"
	cmddoctor "github.com/diggsweden/forge-ci/internal/cli/commands/doctor"
	cmdinit "github.com/diggsweden/forge-ci/internal/cli/commands/initcmd"
	cmdlint "github.com/diggsweden/forge-ci/internal/cli/commands/lint"
	cmdplan "github.com/diggsweden/forge-ci/internal/cli/commands/plan"
	cmdplatform "github.com/diggsweden/forge-ci/internal/cli/commands/platform"
	cmdpolicy "github.com/diggsweden/forge-ci/internal/cli/commands/policy"
	cmdpublish "github.com/diggsweden/forge-ci/internal/cli/commands/publish"
	cmdrelease "github.com/diggsweden/forge-ci/internal/cli/commands/release"
	cmdreport "github.com/diggsweden/forge-ci/internal/cli/commands/report"
	cmdsbom "github.com/diggsweden/forge-ci/internal/cli/commands/sbom"
	cmdsecurity "github.com/diggsweden/forge-ci/internal/cli/commands/security"
	cmdsetup "github.com/diggsweden/forge-ci/internal/cli/commands/setup"
	cmdtoolchain "github.com/diggsweden/forge-ci/internal/cli/commands/toolchain"
	cmdvalidate "github.com/diggsweden/forge-ci/internal/cli/commands/validate"
	cmdversion "github.com/diggsweden/forge-ci/internal/cli/commands/version"
	"github.com/diggsweden/forge-ci/internal/cli/planfile"
	"github.com/diggsweden/forge-ci/internal/clicolor"
	"github.com/diggsweden/forge-ci/internal/docslink"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// flagAuto is the sentinel value for the --provider / --runner / --format
// overrides meaning "defer to environment auto-detection".
const flagAuto = "auto"

// BuildInfo carries ldflags-injected version metadata from main.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// init makes subgroup help category-aware. urfave/cli groups the ROOT help by
// Category but renders a subgroup's help (SubcommandHelpTemplate, used by any
// command that itself has subcommands) as a flat list. Swapping in the
// category-aware command template lets groups that opt into categories (e.g.
// `container`) render grouped sections, while groups without categories fall
// back to the flat list via that template's own {{else}} branch. Terminal
// --help only; the doc generator (render.go) is unaffected.
//
//nolint:gochecknoinits // one-time, process-wide help-template tweak (idempotent under init).
func init() {
	cli.SubcommandHelpTemplate = strings.Replace(
		cli.SubcommandHelpTemplate,
		`{{template "visibleCommandTemplate" .}}`,
		`{{template "visibleCommandCategoryTemplate" .}}`,
		1,
	)

	// Below the root, the global options are one line, not their full
	// usage: the four policy flags alone ran to a dozen wrapped lines on every
	// page and buried the command's own options. The root help keeps them in
	// full. A leaf workflow step also gets a RUN BY section naming the
	// reusable workflows that call it.
	for _, tmpl := range []*string{&cli.CommandHelpTemplate, &cli.SubcommandHelpTemplate} {
		*tmpl = strings.Replace(*tmpl,
			`GLOBAL OPTIONS:{{template "visiblePersistentFlagTemplate" .}}`,
			`GLOBAL OPTIONS:
   {{globalOptionsSummary .}}`, 1)
		*tmpl = strings.Replace(*tmpl,
			`{{template "descriptionTemplate" .}}{{end}}`,
			`{{template "descriptionTemplate" .}}{{end}}{{with runByText .}}

RUN BY:
{{.}}{{end}}`, 1)
	}

	funcs := map[string]any{"runByText": runByText, "globalOptionsSummary": globalOptionsSummary}
	cli.HelpPrinter = func(w io.Writer, templ string, data any) {
		cli.HelpPrinterCustom(w, templ, data, funcs)
	}

	// Advertise required flags in the OPTIONS listing — urfave's default
	// stringer does not mark them, so the requirement is otherwise invisible
	// until the parse fails. Mirrors the reference doc (render.go).
	base := cli.FlagStringer
	cli.FlagStringer = func(f cli.Flag) string {
		s := base(f)
		if rf, ok := f.(cli.RequiredFlag); ok && rf.IsRequired() && s != "" {
			s += " (required)"
		}

		return s
	}
}

// New builds the root *cli.Command. Subgroups are added here as each
// migration phase lands them.
func New(b BuildInfo) *cli.Command {
	// Every docs link the binary prints (check remediations, error hints,
	// help text) targets this binary's release, not main.
	docslink.SetVersion(b.Version)

	versionString := fmt.Sprintf("%s (commit %s, built %s)", b.Version, b.Commit, b.Date)

	root := newRoot(b, versionString)
	// Apply OnUsageError + CommandNotFound to every node so subcommand
	// flag-parse errors get the same concise treatment as root-level
	// ones. urfave/cli does not inherit these hooks automatically.
	applyDiagnosticHooks(root)
	// Reject unexpected positional args on leaf commands (urfave/cli does
	// not do this by default — it silently ignores extras).
	applyArgGuards(root)
	// Make group commands fail closed when invoked without a subcommand
	// (urfave/cli prints help and exits 0 — see applyParentGuards).
	applyParentGuards(root)
	// Order every group's subcommands alphabetically so terminal --help matches
	// the generated reference (cmd/gen-cli-reference already sorts) and lists are
	// predictable.
	sortCommands(root)

	return root
}

// sortCommands orders every node's subcommands alphabetically by name,
// recursively. urfave/cli sorts help categories by name but leaves the commands
// within a group/category in registration order; this applies the same
// alphabetical ordering there, and to non-categorised groups and the root.
func sortCommands(cmd *cli.Command) {
	slices.SortFunc(cmd.Commands, func(a, b *cli.Command) int {
		return strings.Compare(a.Name, b.Name)
	})

	for _, child := range cmd.Commands {
		sortCommands(child)
	}
}

// applyArgGuards walks the tree and wraps every leaf command's Action so an
// unexpected positional argument fails with a usage error (exit 2) instead of
// being silently ignored (urfave/cli's default). Group commands (those with
// subcommands) are skipped. A leaf opts out simply by declaring ArgsUsage —
// the help text for the positional arguments it accepts — so the guard never
// fights a command that genuinely takes args, and the opt-out is the same
// declaration that documents those args.
func applyArgGuards(cmd *cli.Command) {
	if len(cmd.Commands) > 0 {
		for _, child := range cmd.Commands {
			applyArgGuards(child)
		}

		return
	}

	if cmd.Action == nil || cmd.ArgsUsage != "" {
		return
	}

	inner := cmd.Action
	cmd.Action = func(ctx context.Context, leaf *cli.Command) error {
		if leaf.Args().Len() > 0 {
			return fmt.Errorf("unexpected argument %q (this command takes no positional arguments): %w",
				leaf.Args().First(), errs.ErrUsage)
		}

		return inner(ctx, leaf)
	}
}

// applyParentGuards walks the tree and gives every group command (one with
// subcommands but no Action of its own) an Action that fails closed: a usage
// error (exit 2) instead of urfave/cli's default of printing help and exiting
// 0.
//
// Why this matters more than it looks: a workflow step that loses its last
// path segment — `forge-ci validate tag` where `validate tag signature` was
// meant, whether by a typo, a bad edit, or a rename — otherwise passes green
// having validated nothing. No existing guard catches it. The verb path exists,
// so forgejo-ci's test-cli-invocation-contract.sh is satisfied; the surface is
// unchanged, so the CLI guard family and the generated reference are satisfied;
// and exit 0 reads as success to the runner. It is a runtime behaviour, and
// every other check we have inspects surface facts. A gating tool must fail
// closed here.
//
// A typo in the subcommand already fails (`validate tagg` → unknown subcommand,
// exit 2). This closes the omission case, which is the quieter of the two.
//
// The walk starts at root's children because the root has its own Action
// (rootAction): a bare `forge-ci` is refused there, with a start card instead
// of a subcommand list, and the same usage exit code. A group with an Action
// of its own is left alone — that Action is an intentional default.
func applyParentGuards(root *cli.Command) {
	for _, child := range root.Commands {
		applyParentGuard(child)
	}
}

// applyParentGuard applies the fail-closed rule to cmd and, recursively, to
// every command beneath it.
func applyParentGuard(cmd *cli.Command) {
	for _, child := range cmd.Commands {
		applyParentGuard(child)
	}

	if len(cmd.Commands) == 0 || cmd.Action != nil {
		return
	}

	cmd.Action = func(_ context.Context, group *cli.Command) error {
		// `forge-ci version` is the one bare group a person types meaning
		// something else: the tool's own version, which is a flag. That
		// reading comes first, before the list of what the group holds.
		hint := ""
		if group.Name == "version" {
			hint = "the installed forge-ci version is printed by forge-ci --version; "
		}

		return fmt.Errorf("%s%q requires a subcommand (one of: %s): %w",
			hint, group.Name, strings.Join(subcommandNames(group), ", "), errs.ErrUsage)
	}
}

// subcommandNames lists a group's invocable subcommand names for the
// missing-subcommand usage error.
//
// The built-in "help" child is skipped. It is not in the tree cli.New builds,
// which is why an earlier version of this function dropped the skip as
// unreachable; urfave/cli appends it to every group when Run sets the command
// graph up, so by the time this Action runs the child is there and would be
// offered as something to type. TestBareGroupsRefuseThroughRootRun checks the
// message through Run, where it is reachable.
func subcommandNames(group *cli.Command) []string {
	names := make([]string, 0, len(group.Commands))

	for _, child := range group.Commands {
		if child.Name == "help" {
			continue
		}

		names = append(names, child.Name)
	}

	slices.Sort(names)

	return names
}

// applyDiagnosticHooks walks the command tree and sets the
// project-wide OnUsageError hook on every node so the help-dump
// suppression works at any depth.
//
// We deliberately do NOT set CommandNotFound: in urfave/cli v3 a
// non-nil CommandNotFound replaces the default missing-subcommand
// error path entirely, including the error return — so even a noop
// hook causes unknown-subcommand invocations to exit 0 silently.
// The "No help topic for X" message urfave produces by default is
// rewritten to "unknown subcommand X" in cli.ClassifyError instead.
func applyDiagnosticHooks(cmd *cli.Command) {
	cmd.OnUsageError = onUsageError

	for _, child := range cmd.Commands {
		applyDiagnosticHooks(child)
	}
}

func newRoot(info BuildInfo, versionString string) *cli.Command {
	return &cli.Command{
		Name:    "forge-ci",
		Usage:   "forge-neutral CI/CD commands for pull-request and release pipelines",
		Version: versionString,
		// Ships urfave/cli's `completion <bash|zsh|fish|powershell>` helper
		// for interactive use; hidden from help and the generated reference.
		EnableShellCompletion: true,
		// Description appears under the NAME block in help. It states what the
		// tool is, distinguishes the commands meant to be run directly from the
		// ones the reusable workflows invoke internally, and lists the former,
		// deferring the full surface to docs/cli-reference.md and per-subcommand
		// --help.
		Description: rootDescription(info.Version),
		// Suggest enables urfave/cli's built-in Levenshtein-based "did you
		// mean X?" suggestion on unknown subcommands.
		Suggest: true,
		// ExitErrHandler is a no-op so urfave/cli does NOT call os.Exit
		// itself via HandleExitCoder when Action returns an ExitCoder
		// error (e.g. urfave/cli's own "Required flag" / "No help topic"
		// classes). The error flows back to cmd/forge-ci/main.go where
		// it is classified into our sysexits.h-aligned ExitCode ladder.
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		// OnUsageError replaces urfave/cli's default behaviour of dumping
		// the full help text to stderr on a flag-parse failure. The
		// concise "Error: ..." line printed by main.go is enough; the
		// user already knows where help lives.
		OnUsageError: onUsageError,
		Flags: append([]cli.Flag{
			&cli.BoolFlag{
				Name:    "quiet",
				Aliases: []string{"q"},
				// Raises slog's threshold to error. The structured
				// command output that subcommands print directly
				// (`Binary/Module/Version` blocks, build progress
				// lines, …) is NOT affected — that text is the
				// command's result, not log noise. Pipe stderr to
				// /dev/null if you only want machine-readable JSON
				// on stdout.
				Usage:   "raise the log threshold to error (does not suppress command output)",
				Sources: cli.EnvVars("FORGE_CI_QUIET"),
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "logging level: debug, info, warn, error (DEBUG=1/true/yes/on is a shortcut for debug)",
				Value:   "info", //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
				Sources: cli.EnvVars("FORGE_CI_LOG"),
			},
			&cli.StringFlag{
				// --format selects the output format envelope
				// (auto/text/json/github/gitlab). It is intentionally
				// long-form only: clig.dev reserves -o/--output for
				// an output FILE, and subcommands like `publish npm
				// write-npmrc --output -` use --output in that
				// conventional sense.
				Name:    "format",
				Usage:   "output format: " + flagValues(output.All()),
				Value:   string(output.FormatAuto),
				Sources: cli.EnvVars("FORGE_CI_FORMAT"),
			},
			&cli.BoolFlag{
				// --json is the clig.dev-standard shortcut for
				// "give me machine-readable JSON". It is sugar over
				// --format=json and overrides --format if both are
				// set, mirroring the convention used by gh, kubectl,
				// and similar tools.
				Name:    "json",
				Usage:   "shortcut for --format=json (overrides --format); commands with structured results emit JSON, including config parse-artifacts and plan; other commands retain their documented output",
				Sources: cli.EnvVars("FORGE_CI_JSON"),
			},
			&cli.BoolFlag{
				// --no-color forces the success/failure glyphs to plain.
				// Color is otherwise auto-disabled off a TTY and by the
				// cross-tool NO_COLOR / TERM=dumb conventions, which the
				// clicolor package honours directly.
				Name:    "no-color",
				Usage:   "disable colored ✓/✗ output (also: $NO_COLOR, non-terminal output)",
				Sources: cli.EnvVars("FORGE_CI_NO_COLOR"),
			},
			&cli.StringFlag{
				// --provider overrides forge-API auto-detection. Needed
				// because Forgejo Actions masquerades as GitHub
				// (GITHUB_ACTIONS=true); an adopter on a custom or
				// misreporting runner can force the correct forge.
				// "auto" (the default) defers to env detection.
				Name:    "provider",
				Usage:   "forge API override: " + providerFlagValues(),
				Value:   flagAuto,
				Sources: cli.EnvVars("FORGE_CI_PROVIDER"),
			},
			&cli.StringFlag{
				// --runner overrides runner-convention auto-detection
				// (which output dialect to emit), independent of
				// --provider. "auto" defers to env detection.
				Name:    "runner",
				Usage:   "runner conventions override: " + runnerFlagValues(),
				Value:   flagAuto,
				Sources: cli.EnvVars("FORGE_CI_RUNNER"),
			},
		}, adminPolicyFlags()...),
		// Action handles the two things that reach the root itself: no command
		// at all, and a first argument that is not a command. See rootAction.
		// It gets the bare version (info.Version), not cmd.Version, which is
		// the composed "v0.90.0 (commit …, built …)" string: docslink.RefFor must see
		// the tag alone, or a release build prints a broken Docs URL.
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return rootAction(ctx, cmd, info.Version)
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if err := configureLogger(cmd); err != nil {
				return ctx, err
			}

			if cmd.Bool("no-color") {
				clicolor.Disable()
			}
			// --json wins over --format. The same precedence rule
			// lives in deps.rawFormat for non-root call sites that
			// must reach through cmd.Root() — keep both in sync.
			raw := cmd.String("format")
			if cmd.Bool("json") {
				raw = string(output.FormatJSON)
			}

			if _, err := output.Parse(raw); err != nil {
				return ctx, fmt.Errorf("%w: %w", err, errs.ErrUsage)
			}

			if err := applyProviderOverrides(cmd); err != nil {
				return ctx, err
			}

			// `plan write` reads the file itself: it merges into a plan,
			// refuses one it cannot read, and with --new replaces it.
			if args := cmd.Args().Slice(); len(args) < 2 || args[0] != "plan" || args[1] != "write" {
				if err := planfile.Check(); err != nil {
					return ctx, err
				}
			}

			return withAdminPolicy(ctx, cmd), nil
		},
		// The root help groups the tree by audience. A person setting up or
		// troubleshooting a repository works in the first category; the second
		// is the surface the reusable workflows call, listed in full so the
		// reference stays complete. The category names sort alphabetically in
		// urfave/cli's help, so "Repository" is named to land first.
		Commands: slices.Concat(
			cmdmeta.WithCategory(categoryRepository,
				cmdconfig.New(),
				cmddocs.New(),
				cmddoctor.New(info.Commit),
				cmdinit.New(),
				cmdsetup.New(info.Commit),
			),
			cmdmeta.WithCategory(categoryWorkflow,
				cmdartifact.New(),
				cmdbuild.New(),
				cmdlint.New(),
				cmdplatform.New(),
				cmdpolicy.New(),
				cmdcontainer.New(),
				cmdplan.New(),
				cmdpublish.New(),
				cmdrelease.New(),
				cmdsbom.New(),
				cmdsecurity.New(),
				cmdtoolchain.New(),
				cmdreport.New(),
				cmdvalidate.New(),
				cmdversion.New(),
			),
		),
	}
}

// Root help categories. The names are part of the help text, and their
// alphabetical order is their display order.
const (
	categoryRepository = "Repository setup and troubleshooting"
	categoryWorkflow   = "Workflow steps"
)

// ErrNoCommand is what a bare `forge-ci` returns: the start card has already
// been written to stderr, so main prints no further line. It wraps
// errs.ErrUsage, so the exit code is the same as for a bare command group.
var ErrNoCommand = fmt.Errorf("forge-ci needs a command: %w", errs.ErrUsage)

// rootAction runs when no subcommand matched. Two cases reach it.
//
// No arguments at all: the bare binary. Siblings in this tool family treat
// that as a usage error with a short start card on stderr, not as a request
// for the reference, and so does forge-ci: the card names the two commands a
// person runs first and leaves the full surface to --help. The exit code is
// usage (2), the same as a bare command group, so a workflow step that lost
// every path segment fails just like one that lost its last.
//
// A first argument that is no command: a typo. urfave/cli's default root
// action reported that as "No help topic for 'X'" plus a suggestion; this
// reproduces the user-facing form that ClassifyError made of it, so the
// stderr line is unchanged.
func rootAction(_ context.Context, cmd *cli.Command, version string) error {
	if cmd.Args().Len() > 0 {
		name := cmd.Args().First()
		msg := fmt.Sprintf("unknown subcommand '%s'", name)

		if suggestion := cli.SuggestCommand(cmd.Commands, name); suggestion != "" {
			msg += fmt.Sprintf(". Did you mean %q?", suggestion)
		}

		return fmt.Errorf("%s: %w", msg, errs.ErrUsage)
	}

	_, _ = fmt.Fprint(cmd.Root().ErrWriter, rootCard(version))

	return ErrNoCommand
}

// porcelainCommands lists the commands intended to be run directly, as opposed
// to the commands the shared CI/CD workflows invoke internally (the full set
// is in docs/cli-reference.md). The first two are the start of an adoption and
// head the bare-invocation card; the rest follow on the card and in
// `forge-ci --help`. TestPorcelainCommandsResolve asserts every path still
// resolves to a real command, so the list cannot silently drift from the tree.
type porcelainCommand struct {
	path string // space-separated command path, e.g. "config validate"
	what string
}

var porcelainCommands = []porcelainCommand{
	{"init", "write a repository's first Forge CI files from an example"},
	{"doctor", "check that a repository is configured correctly for forge-ci"},
	{"setup status", "list what is left before a first release, and the command for each step"},
	{"docs", "print the documentation shipped in this binary"},
	{"config validate", "validate .forge-ci/artifacts.yml against the schema"},
	{"container login", "store container registry credentials"},
	{"platform checkout", "check out a repository without persisting credentials"},
	{"sbom assemble", "assemble the project's SBOM layer set after a build"},
	{"version bump", "update the recorded project version"},
}

// startHereCount is how many leading porcelainCommands form the START HERE
// block of the card: init, doctor, then setup status.
const startHereCount = 3

// rootCard renders what a bare `forge-ci` prints: what the tool is, the two
// commands to run first, the other commands meant for a terminal, and where
// the rest lives. It is deliberately short (the house budget for brief help is
// about fifteen lines) and goes to stderr, since a bare invocation is a usage
// error, not a request for the reference.
func rootCard(version string) string {
	var card strings.Builder

	card.WriteString("forge-ci - forge-neutral CI/CD commands for pull-request and release pipelines\n\n")
	card.WriteString("START HERE\n")
	// The two commands to type are written in full so they can be pasted; the
	// rest are names to recognise, listed bare as the sibling tools do. Every
	// row fits an 80-column terminal, so the card is as tall as it looks.
	fmt.Fprintf(&card, "  %-40s %s\n", "forge-ci init <forge-ci URL> --dry-run", "preview the first Forge CI files")
	fmt.Fprintf(&card, "  %-40s %s\n", "forge-ci doctor", "check the Forge CI configuration")
	fmt.Fprintf(&card, "  %-40s %s\n", "forge-ci setup status", "what is left before a first release")
	card.WriteString("\nALSO RUN DIRECTLY\n")

	for _, c := range porcelainCommands[startHereCount:] {
		if c.path == "docs" {
			continue // the Docs line below names it; the card stays within its line budget
		}

		fmt.Fprintf(&card, "  %-19s %s\n", c.path, c.what)
	}

	card.WriteString("\nVerify a release you downloaded: forge-ci validate artifact-signature --help\n")
	card.WriteString("The other commands are mostly workflow steps; see forge-ci --help.\n")
	fmt.Fprintf(&card, "Docs:    forge-ci docs (offline), https://github.com/diggsweden/forge-ci/tree/%s/docs\n", docslink.RefFor(version))
	card.WriteString("Issues:  https://github.com/diggsweden/forge-ci/issues\n")

	return card.String()
}

// rootDescription renders the root --help body: a short statement of what the
// tool is, the commands meant to be run directly, and the docs/issues links.
// The Docs link targets the installed version's tag (workflows pin forge-ci
// to a tag, so a v0.90.0 binary points at v0.90.0 docs, not main — clig.dev
// §Documentation); dev builds fall back to main via docslink.RefFor.
func rootDescription(version string) string {
	var common strings.Builder
	for _, c := range porcelainCommands {
		fmt.Fprintf(&common, "   forge-ci %-18s %s\n", c.path, c.what)
	}

	return fmt.Sprintf(`forge-ci runs the steps of a CI/CD pull-request or release pipeline: building
and testing projects, generating SBOMs, building and signing container images,
scanning for vulnerabilities, and publishing releases. It behaves the same on
GitHub, GitLab, and Forgejo.

Most commands are run by the reusable workflows inside a CI job, where they
auto-detect the forge and read the repository, run, and credentials from the
job environment; --provider and --runner override the detection. The commands
below are the ones to run directly, to set up or troubleshoot a repository:

%s
To verify a release you downloaded, run forge-ci validate artifact-signature
(release files) or forge-ci validate container-signature (images).

To trim old releases' files or old container images, run forge-ci release
asset-retention or forge-ci container package-retention; both only report
until you pass --dry-run=false.

Run "forge-ci <command> --help" for a command's options; the full command
reference is forge-ci docs cli-reference. Shell completion: forge-ci
completion bash|zsh|fish|powershell.

Exit codes:
%s
Docs:    forge-ci docs (offline), https://github.com/diggsweden/forge-ci/tree/%s/docs
Issues:  https://github.com/diggsweden/forge-ci/issues`, common.String(), exitCodeTable(), docslink.RefFor(version))
}

// exitCodeTable lists the exit codes errs.ExitCodeFromError returns, so a
// script author reads them from the binary rather than from its source.
func exitCodeTable() string {
	rows := []struct {
		code errs.ExitCodeType
		what string
	}{
		{errs.ExitCodeOK, "success"},
		{errs.ExitCodeValidation, "a check failed (doctor, validators, gates)"},
		{errs.ExitCodeUsage, "wrong command, flag or argument; interrupted"},
		{errs.ExitCodeDataErr, "malformed input file or value"},
		{errs.ExitCodeNoInput, "missing input file, ref or release"},
		{errs.ExitCodeUnavailable, "a registry, forge or download host could not be reached, rate limited; retry may help"},
		{errs.ExitCodeSoftware, "internal error; please report it"},
		{errs.ExitCodeNoPerm, "permission denied (token scope, file mode)"},
		{errs.ExitCodeConfiguration, "invalid configuration, a required tool missing, or unsupported here; retrying will not help"},
	}

	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "   %-3d %s\n", r.code, r.what)
	}

	return b.String()
}

// applyProviderOverrides validates the --provider / --runner flags and
// bridges their argv values into the FORGE_CI_PROVIDER /
// FORGE_CI_RUNNER env vars that internal/adapters/platform reads. Detection
// lives in one place (the platform package); the flags are sugar over
// the env, so bridging argv → env keeps a single source of truth. The
// env-var names must match the override constants in internal/adapters/platform
// and the flags' EnvVars sources above.
func applyProviderOverrides(cmd *cli.Command) error {
	if err := bridgeOverride(cmd, "provider", "FORGE_CI_PROVIDER",
		func(v string) bool { return provider.ForgeAPI(v).IsValid() },
		providerFlagValues()); err != nil {
		return err
	}

	return bridgeOverride(cmd, "runner", "FORGE_CI_RUNNER",
		func(v string) bool { return provider.RunnerKind(v).IsValid() },
		runnerFlagValues())
}

// flagValues renders an enum's values as a comma-separated help string.
// flagValues / providerFlagValues / runnerFlagValues derive from the
// domain's canonical sets (output.All, provider.AllForges,
// provider.AllRunnerKinds) so help text and validation never drift from
// the enums they describe.
func flagValues[T ~string](vals []T) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}

	return strings.Join(out, ", ")
}

// providerFlagValues / runnerFlagValues prepend the CLI-only "auto"
// sentinel (which is not a domain ForgeAPI/RunnerKind value) to the
// canonical set.
func providerFlagValues() string {
	return flagAuto + ", " + flagValues(provider.AllForges())
}

func runnerFlagValues() string {
	return flagAuto + ", " + flagValues(provider.AllRunnerKinds())
}

// bridgeOverride validates one override flag and, when it was set on the
// command line, mirrors its normalised value into envName so
// internal/adapters/platform (which reads only env) observes it. "auto" and the
// empty string defer to auto-detection. valid reports whether a concrete
// value is recognised; want is the accepted-values hint for the error.
func bridgeOverride(cmd *cli.Command, flag, envName string, valid func(string) bool, want string) error {
	raw := strings.ToLower(strings.TrimSpace(cmd.String(flag)))
	if raw != "" && raw != flagAuto && !valid(raw) {
		return fmt.Errorf("invalid --%s %q (want %s): %w", flag, cmd.String(flag), want, errs.ErrUsage)
	}

	if cmd.IsSet(flag) {
		if err := os.Setenv(envName, raw); err != nil {
			return fmt.Errorf("set %s override: %w", flag, err)
		}
	}

	return nil
}

// configureLogger wires slog to stderr at the requested level.
// Quiet mode raises the threshold to error.
func configureLogger(cmd *cli.Command) error {
	level := resolveLogLevel(
		cmd.String("log-level"),
		cmd.IsSet("log-level"),
		cmd.Bool("quiet"),
		isTruthyEnv("DEBUG"),
	)

	var slogLevel slog.Level

	switch strings.ToLower(level) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "info":
		slogLevel = slog.LevelInfo
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		return fmt.Errorf("invalid log-level %q (want debug/info/warn/error): %w", level, errs.ErrUsage)
	}

	// clig.dev §Output: "Don't treat stderr like a log file, at least
	// not by default." At info/warn/error we render one undecorated
	// line per record via plainHandler; at debug we keep the structured
	// TextHandler so operators piping stderr to a file still get
	// timestamps and attrs.
	var handler slog.Handler
	if slogLevel == slog.LevelDebug {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slogLevel})
	} else {
		handler = newPlainHandler(os.Stderr, slogLevel)
	}

	slog.SetDefault(slog.New(handler))

	return nil
}

// isTruthyEnv reports whether the named env var holds a conventional
// "truthy" value (1, true, yes, on — case-insensitive). The empty
// string and any other value is falsy. Matches the convention used by
// most CLI tooling for $DEBUG / $CI / $NO_COLOR-style flags.
func isTruthyEnv(name string) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "1", "true", "yes", "on":
		return true
	}

	return false
}

// resolveLogLevel returns the effective slog level string given the
// flag value, whether the flag was explicitly set (CLI or env source),
// the quiet boolean, and whether $DEBUG is truthy.
//
// Precedence (highest to lowest):
//
//  1. --quiet → "error" (always wins; matches existing UX)
//  2. --log-level on argv, or $FORGE_CI_LOG (urfave's IsSet covers both)
//  3. $DEBUG truthy → "debug" (clig.dev general-purpose env-var convention)
//  4. the "info" default
//
// Pure function for testability — no global slog state, no env reads
// beyond what the caller passes in.
func resolveLogLevel(flagLevel string, flagSet, quiet, debugTruthy bool) string {
	if quiet {
		return "error"
	}

	if !flagSet && flagLevel == "info" && debugTruthy {
		return "debug"
	}

	return flagLevel
}

// onUsageError replaces urfave/cli's default behaviour of dumping the
// full help text to stderr on a flag-parse failure. We print the
// error itself + a one-line pointer to --help, then return the error
// so main.go can also set the exit code. The IsAlreadyPrintedByCLI
// check there suppresses the duplicate "Error: ..." line.
//
// helpPath is computed from the command hierarchy so subcommand
// failures point at the right help.
func onUsageError(_ context.Context, cmd *cli.Command, err error, _ bool) error {
	_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
	_, _ = fmt.Fprintf(os.Stderr, "Run '%s --help' for available flags.\n", helpPath(cmd))

	// Only a missing required flag means "you ran a workflow step by hand";
	// a malformed value is the same mistake in CI.
	if hint := runByHint(cmd); hint != "" && strings.Contains(err.Error(), "Required flag") {
		_, _ = fmt.Fprintln(os.Stderr, hint)
	}

	return err
}

// helpPath returns the space-separated command path the user should
// pass to --help. urfave/cli's FullName() already produces this
// (parent commands joined with space); we just guard against a nil
// receiver so the helpers stay defensive.
func helpPath(cmd *cli.Command) string {
	if cmd == nil {
		return "forge-ci"
	}

	return cmd.FullName()
}

// globalOptionsSummary is the short GLOBAL OPTIONS of a subcommand's help:
// every global flag by name, and where their full usage is. The names stay
// so a reader (and the engine contract test) can see a flag is accepted.
func globalOptionsSummary(data any) string {
	cmd, ok := data.(*cli.Command)
	if !ok {
		return "see forge-ci --help"
	}

	var names []string

	for _, f := range cmd.VisiblePersistentFlags() {
		names = append(names, "--"+f.Names()[0])
	}

	return wrapList(names, 76, "\n   ") + "\n   Full descriptions: forge-ci --help"
}

// wrapList joins items with ", ", breaking with sep before a line passes width.
func wrapList(items []string, width int, sep string) string {
	var (
		builder strings.Builder
		line    int
	)

	for i, item := range items {
		piece := item
		if i < len(items)-1 {
			piece += ","
		}

		if line > 0 && line+1+len(piece) > width {
			builder.WriteString(sep)

			line = 0
		} else if line > 0 {
			builder.WriteString(" ")

			line++
		}

		builder.WriteString(piece)
		line += len(piece)
	}

	return builder.String()
}
