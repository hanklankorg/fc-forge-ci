// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package toolchain wires bootstrap/toolchain management commands.
package toolchain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/diggsweden/forge-ci/internal/adapters/apt"
	"github.com/diggsweden/forge-ci/internal/adapters/mise"
	"github.com/urfave/cli/v3"

	apptoolchain "github.com/diggsweden/forge-ci/internal/app/toolchain"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/cli/planfile"
)

// Plan-file scopes of the flag-heavy toolchain verbs in $FORGE_CI_PLAN
// (flag > plan > env > default). One const per command; the scope string is
// the command path.
const (
	planScopeInstallMise              = "toolchain install-mise"
	planScopeInstallMiseTools         = "toolchain install-mise-tools"
	planScopeInstallChangelogRenderer = "toolchain install-changelog-renderer"
	planScopeSetupMiseEnv             = "toolchain setup-mise-env"

	// flagTempDir is the scratch-directory flag.
	flagTempDir = "temp-dir"
)

// Shared flag names, defaults and usage strings across the toolchain
// subcommands, declared once so spellings cannot drift and the package stays
// under goconst's literal budget. Values are part of the CLI contract —
// docs/cli-reference.md is generated from them and a sync test gates any
// change.
const (
	flagMiseRoot = "root"
	flagLocked   = "locked"
	flagBinHome  = "bin-home"
	flagPathFile = "path-file"
	// lockedDefault is the shared default for the --locked flags.
	lockedDefault = "false"
	// usageMiseRoot is the shared usage string for the --root flags.
	usageMiseRoot = "repository root containing mise config"
)

// New returns the `toolchain` subgroup command tree.
func New() *cli.Command {
	return &cli.Command{
		Name:  "toolchain",
		Usage: "bootstrap and expose CI toolchains",
		Commands: []*cli.Command{
			cacheDiscriminatorCmd(),
			exposeMiseToolsCmd(),
			installChangelogRendererCmd(),
			installMiseCmd(),
			installMiseToolsCmd(),
			installSystemDependenciesCmd(),
			setupGoEnvCmd(),
			setupMiseEnvCmd(),
			trustMiseConfigCmd(),
			validateMiseInstallCmd(),
		},
	}
}

func cacheDiscriminatorCmd() *cli.Command {
	return &cli.Command{
		Name:  "cache-discriminator",
		Usage: "emit setup-toolchain's stable tool-cache discriminator",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "tools", Sources: cli.EnvVars("TOOLSET_TOOLS"), Usage: "whitespace-separated mise tool selectors requested by the caller"},
			&cli.StringFlag{Name: "install-dev-tools", Sources: cli.EnvVars("TOOLSET_INSTALL_DEV_TOOLS"), Usage: "whether consumer dev-tool installers are enabled"},
			&cli.StringFlag{Name: "extra-cache-paths", Sources: cli.EnvVars("TOOLSET_EXTRA_CACHE_PATHS"), Usage: "additional newline-separated cache paths"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			hash := apptoolchain.CacheDiscriminator(apptoolchain.CacheDiscriminatorInput{
				Tools:           cmd.String("tools"),
				InstallDevTools: cmd.String("install-dev-tools"),
				ExtraCachePaths: cmd.String("extra-cache-paths"),
			})
			_, _ = fmt.Fprintln(os.Stdout, hash)

			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return d.OutputSink.Set(ctx, "hash", hash)
			})
		},
	}
}

func exposeMiseToolsCmd() *cli.Command {
	return &cli.Command{
		Name:  "expose-mise-tools",
		Usage: "expose installed mise tool binaries to later CI steps",
		Description: `Lists installed mise tools, appends each existing mise bin-path to the
runner path file, symlinks executable files into ~/.local/bin, and exposes
rustup-managed cargo/rustc bins when the repository declares rustup and
rust-toolchain.toml. Intended to run unconditionally after cache restore/install.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagMiseRoot, Value: ".", Sources: cli.EnvVars("MISE_PROJECT_ROOT"), Usage: usageMiseRoot},
			&cli.StringFlag{Name: flagBinHome, Sources: cli.EnvVars("TOOLCHAIN_BIN_HOME"), Usage: "directory where executable symlinks are written (default: $HOME/.local/bin)"},
			&cli.StringFlag{Name: flagPathFile, Required: true, Sources: cli.EnvVars("FORGEJO_PATH", "GITHUB_PATH"), Usage: "runner path file to append exposed bin directories to"},
			&cli.StringFlag{Name: flagLocked, Value: lockedDefault, Sources: cli.EnvVars("MISE_LOCKED_INSTALL"), Usage: "whether rustup cargo exposure should use locked mise mode: true|false"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return apptoolchain.ExposeMiseTools(ctx, mise.New(), os.Stdout, apptoolchain.ExposeMiseToolsInput{
				Root:     cmd.String(flagMiseRoot),
				BinHome:  cmd.String(flagBinHome),
				PathFile: cmd.String(flagPathFile),
				Locked:   cmd.String(flagLocked),
			})
		},
	}
}

func installMiseToolsCmd() *cli.Command {
	return &cli.Command{
		Name:  "install-mise-tools",
		Usage: "install mise-managed CI tools with runtime bootstrap and retry",
		Description: `Runs the setup-toolchain mise install sequence: sanitize child env,
install declared backend runtimes (uv/go/rust/rustup) first, then a
caller-selected tool subset or the full config, retrying each mise install
and the rustup calls that download the toolchain rust-toolchain.toml pins.
Every mise call
runs with MISE_HTTP_TIMEOUT set from --http-timeout-seconds: the time mise waits
for a connection or between two reads of a response, 30 seconds by mise's own
default. Tokens are inherited
only as mise-specific env vars and are never passed on argv. Every flag may also be
fed from the $FORGE_CI_PLAN plan file under the "toolchain install-mise-tools"
scope (flag > plan > env > default).`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagMiseRoot, Value: ".", Sources: planfile.Vars(planScopeInstallMiseTools, flagMiseRoot, "MISE_PROJECT_ROOT"), Usage: usageMiseRoot},
			&cli.StringFlag{Name: flagLocked, Value: lockedDefault, Sources: planfile.Vars(planScopeInstallMiseTools, flagLocked, "MISE_LOCKED_INSTALL"), Usage: "whether mise install should use locked mode: true|false"},
			&cli.StringFlag{Name: "tools", Sources: planfile.Vars(planScopeInstallMiseTools, "tools", "TOOLS_SUBSET", "MISE_TOOLS"), Usage: "whitespace-separated mise tool selectors to install instead of the full config"},
			&cli.StringFlag{Name: "mise-bin", Sources: planfile.Vars(planScopeInstallMiseTools, "mise-bin", "MISE_BIN"), Usage: "mise binary path (default: $HOME/.local/bin/mise when present, else mise from PATH)"},
			&cli.IntFlag{Name: "retry-attempts", Value: 3, Sources: planfile.Vars(planScopeInstallMiseTools, "retry-attempts", "MISE_INSTALL_RETRY_ATTEMPTS"), Usage: "attempts for each mise install and rustup toolchain download"},
			&cli.IntFlag{Name: "retry-delay-seconds", Value: 10, Sources: planfile.Vars(planScopeInstallMiseTools, "retry-delay-seconds", "MISE_INSTALL_RETRY_DELAY_SECONDS"), Usage: "seconds between mise install retry attempts"},
			&cli.IntFlag{Name: "http-timeout-seconds", Value: 120, Sources: planfile.Vars(planScopeInstallMiseTools, "http-timeout-seconds", "MISE_INSTALL_HTTP_TIMEOUT_SECONDS"), Usage: "seconds mise waits for a connection or between reads of a download (MISE_HTTP_TIMEOUT)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			runner := mise.New()
			runner.Bin = resolveMiseBin(cmd.String("mise-bin"))

			return apptoolchain.InstallMiseTools(ctx, runner, os.Stdout, apptoolchain.InstallMiseToolsInput{
				Root:          cmd.String(flagMiseRoot),
				Locked:        cmd.String(flagLocked),
				Tools:         cmd.String("tools"),
				RetryAttempts: cmd.Int("retry-attempts"),
				RetryDelay:    time.Duration(cmd.Int("retry-delay-seconds")) * time.Second,
				HTTPTimeout:   time.Duration(cmd.Int("http-timeout-seconds")) * time.Second,
			})
		},
	}
}

func installChangelogRendererCmd() *cli.Command {
	return &cli.Command{
		Name:  "install-changelog-renderer",
		Usage: "install a pinned changelog renderer from its verified release archive",
		Description: `Downloads the Linux release archive of git-chglog or git-cliff at the pinned
version, verifies it against the pinned SHA-256 for the runner's architecture,
installs the renderer into the bin home and appends that directory to the
runner PATH file for subsequent steps. The archive comes from the renderer's
own release URL: no tool manager runs, no forge API is called and no token is
read, so the install is the same in a fork and in an isolated rebuild. Both
checksums are required, and every local refusal precedes the download.
Every flag may also be fed from the $FORGE_CI_PLAN plan file under the
"toolchain install-changelog-renderer" scope (flag > plan > env > default).`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "changelog-creator", Required: true, Sources: planfile.Vars(planScopeInstallChangelogRenderer, "changelog-creator", "CHANGELOG_CREATOR"), Usage: "changelog renderer: git-cliff or git-chglog"},
			&cli.StringFlag{Name: "version", Required: true, Sources: planfile.Vars(planScopeInstallChangelogRenderer, "version", "CHANGELOG_RENDERER_VERSION"), Usage: "renderer version without leading v"},
			&cli.StringFlag{Name: "linux-x64-sha256", Required: true, Sources: planfile.Vars(planScopeInstallChangelogRenderer, "linux-x64-sha256", "CHANGELOG_RENDERER_LINUX_X64_SHA256"), Usage: "SHA-256 of the renderer's linux x86-64 release archive"},
			&cli.StringFlag{Name: "linux-arm64-sha256", Required: true, Sources: planfile.Vars(planScopeInstallChangelogRenderer, "linux-arm64-sha256", "CHANGELOG_RENDERER_LINUX_ARM64_SHA256"), Usage: "SHA-256 of the renderer's linux arm64 release archive"},
			&cli.StringFlag{Name: "base-url", Sources: planfile.Vars(planScopeInstallChangelogRenderer, "base-url", "CHANGELOG_RENDERER_BASE_URL"), Usage: "override the renderer's release base URL for tests/mirrors"},
			&cli.StringFlag{Name: flagBinHome, Sources: planfile.Vars(planScopeInstallChangelogRenderer, flagBinHome, "TOOLCHAIN_BIN_HOME"), Usage: "directory the renderer is installed into (default: $HOME/.local/bin)"},
			&cli.StringFlag{Name: flagPathFile, Required: true, Sources: planfile.Vars(planScopeInstallChangelogRenderer, flagPathFile, "FORGEJO_PATH", "GITHUB_PATH"), Usage: "runner path file to append the bin directory to"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return apptoolchain.InstallChangelogRenderer(ctx, nil, os.Stdout, apptoolchain.InstallChangelogRendererInput{
				Backend:          cmd.String("changelog-creator"),
				Version:          cmd.String("version"),
				LinuxX64SHA256:   cmd.String("linux-x64-sha256"),
				LinuxARM64SHA256: cmd.String("linux-arm64-sha256"),
				BaseURL:          cmd.String("base-url"),
				BinHome:          cmd.String(flagBinHome),
				PathFile:         cmd.String(flagPathFile),
			})
		},
	}
}

func setupGoEnvCmd() *cli.Command {
	return &cli.Command{
		Name:  "setup-go-env",
		Usage: "choose setup-toolchain's Go provider and write the Go env for mise's Go",
		Description: `Emits the "provider" output: "mise" when the repository's mise config
declares go, so that the mise install brings Go (from mise.lock in locked mode),
and "go.mod" otherwise. setup-toolchain installs Go from go.mod only in the
second case, so a job downloads Go once. For "mise" it also writes
GOTOOLCHAIN=local to the runner env file and GOPATH/bin to the runner path file.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagMiseRoot, Value: ".", Sources: cli.EnvVars("MISE_PROJECT_ROOT"), Usage: usageMiseRoot},
			&cli.StringFlag{Name: flagPathFile, Required: true, Sources: cli.EnvVars("FORGEJO_PATH", "GITHUB_PATH"), Usage: "runner path file to append GOPATH/bin to"},
			&cli.StringFlag{Name: "env-file", Required: true, Sources: cli.EnvVars("FORGEJO_ENV", "GITHUB_ENV"), Usage: "runner env file receiving GOTOOLCHAIN"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			provider, err := apptoolchain.SetupGoEnv(apptoolchain.SetupGoEnvInput{
				Root:     cmd.String(flagMiseRoot),
				PathFile: cmd.String(flagPathFile),
				EnvFile:  cmd.String("env-file"),
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintln(os.Stdout, provider)

			return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
				return d.OutputSink.Set(ctx, "provider", provider)
			})
		},
	}
}

func setupMiseEnvCmd() *cli.Command {
	return &cli.Command{
		Name:  "setup-mise-env",
		Usage: "write setup-toolchain mise PATH/env file entries",
		Description: `Every flag may also be fed from the $FORGE_CI_PLAN plan file under
the "toolchain setup-mise-env" scope (flag > plan > env > default).`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cache", Value: "true", Sources: planfile.Vars(planScopeSetupMiseEnv, "cache", "SETUP_CACHE"), Usage: "setup-toolchain cache mode: true|false"},
			&cli.StringFlag{Name: flagBinHome, Sources: planfile.Vars(planScopeSetupMiseEnv, flagBinHome, "TOOLCHAIN_BIN_HOME"), Usage: "directory containing exposed tool symlinks (default: $HOME/.local/bin)"},
			&cli.StringFlag{Name: flagPathFile, Required: true, Sources: planfile.Vars(planScopeSetupMiseEnv, flagPathFile, "FORGEJO_PATH", "GITHUB_PATH"), Usage: "runner path file to append PATH entries to"},
			&cli.StringFlag{Name: "env-file", Required: true, Sources: planfile.Vars(planScopeSetupMiseEnv, "env-file", "FORGEJO_ENV", "GITHUB_ENV"), Usage: "runner env file receiving MISE_* directory exports"},
			&cli.StringFlag{Name: flagTempDir, Sources: planfile.Chain(planScopeSetupMiseEnv, flagTempDir, cienv.TempDir()), Usage: "scratch directory for the isolated mise dirs"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return apptoolchain.SetupMiseEnv(apptoolchain.SetupMiseEnvInput{
				Cache:      cmd.String("cache"),
				BinHome:    cmd.String(flagBinHome),
				PathFile:   cmd.String(flagPathFile),
				EnvFile:    cmd.String("env-file"),
				RunnerTemp: cmd.String(flagTempDir),
			})
		},
	}
}

func resolveMiseBin(value string) string {
	if value != "" {
		return value
	}

	home, err := os.UserHomeDir()
	if err == nil {
		candidate := filepath.Join(home, ".local", "bin", "mise")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
	}

	return "mise"
}

func installSystemDependenciesCmd() *cli.Command {
	return &cli.Command{
		Name:  "install-system-dependencies",
		Usage: "install apt bootstrap packages when apt-get is available",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "packages", Required: true, Sources: cli.EnvVars("SYSTEM_DEPENDENCY_PACKAGES"), Usage: "whitespace-separated apt package names to install"},
			&cli.BoolFlag{Name: "skip-if-missing-apt", Value: true, Sources: cli.EnvVars("SKIP_IF_MISSING_APT"), Usage: "skip successfully when apt-get is unavailable"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return apptoolchain.InstallSystemDependencies(ctx, apt.New(), os.Stdout, apptoolchain.InstallSystemDependenciesInput{
				Packages:         cmd.String("packages"),
				SkipIfMissingAPT: cmd.Bool("skip-if-missing-apt"),
			})
		},
	}
}

func trustMiseConfigCmd() *cli.Command {
	return &cli.Command{
		Name:  "trust-mise-config",
		Usage: "trust the consumer repository mise config in the current directory",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "mise-bin", Sources: cli.EnvVars("MISE_BIN"), Usage: "mise binary path (default: $HOME/.local/bin/mise when present, else mise from PATH)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			runner := mise.New()
			runner.Bin = resolveMiseBin(cmd.String("mise-bin"))

			return apptoolchain.TrustMiseConfig(ctx, runner, os.Stdout)
		},
	}
}

func installMiseCmd() *cli.Command {
	return &cli.Command{
		Name:  "install-mise",
		Usage: "download, checksum-verify, and install the pinned mise binary",
		Description: `Installs mise from the official linux-<arch>-musl release
archive after verifying a caller-owned SHA-256 pin. The version and checksums are
passed as flags so the consuming CI template owns its pin/update policy while
forge-ci owns the download, checksum, archive-shape, and install mechanics.
Every flag may also be fed from the $FORGE_CI_PLAN plan file under the
"toolchain install-mise" scope (flag > plan > env > default).`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "version", Required: true, Sources: planfile.Vars(planScopeInstallMise, "version", "MISE_VERSION"), Usage: "mise version without leading v"},
			&cli.StringFlag{Name: "linux-x64-sha256", Required: true, Sources: planfile.Vars(planScopeInstallMise, "linux-x64-sha256", "MISE_LINUX_X64_MUSL_TAR_GZ_SHA256"), Usage: "SHA-256 of mise-v<version>-linux-x64-musl.tar.gz"},
			&cli.StringFlag{Name: "linux-arm64-sha256", Required: true, Sources: planfile.Vars(planScopeInstallMise, "linux-arm64-sha256", "MISE_LINUX_ARM64_MUSL_TAR_GZ_SHA256"), Usage: "SHA-256 of mise-v<version>-linux-arm64-musl.tar.gz"},
			&cli.StringFlag{Name: "dest-dir", Sources: planfile.Vars(planScopeInstallMise, "dest-dir", "MISE_INSTALL_DEST_DIR"), Usage: "installation directory (default: $HOME/.local/bin)"},
			&cli.StringFlag{Name: "base-url", Sources: planfile.Vars(planScopeInstallMise, "base-url", "MISE_RELEASE_BASE_URL"), Usage: "override release base URL for tests/mirrors (default: GitHub mise releases)"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			_, err := apptoolchain.InstallMise(ctx, nil, os.Stderr, apptoolchain.InstallMiseInput{
				Version:          cmd.String("version"),
				LinuxX64SHA256:   cmd.String("linux-x64-sha256"),
				LinuxARM64SHA256: cmd.String("linux-arm64-sha256"),
				DestDir:          cmd.String("dest-dir"),
				BaseURL:          cmd.String("base-url"),
			})

			return err
		},
	}
}

func validateMiseInstallCmd() *cli.Command {
	return &cli.Command{
		Name:  "validate-mise-install",
		Usage: "validate setup-toolchain's mise install mode before token-bearing work",
		Description: `Checks the mise install policy used by CI setup wrappers: locked mode
requires a committed mise.lock, unlocked repositories with mise config require the
caller to confirm a GitHub rate-limit token is present, and locked backend tools
must pin their runtime (uv for pipx, go for go, rust or rustup+rust-toolchain for cargo).`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagMiseRoot, Value: ".", Sources: cli.EnvVars("MISE_PROJECT_ROOT"), Usage: usageMiseRoot},
			&cli.StringFlag{Name: flagLocked, Value: lockedDefault, Sources: cli.EnvVars("MISE_LOCKED_INSTALL"), Usage: "whether mise install will run in locked mode: true|false"},
			&cli.BoolFlag{Name: "github-token-present", Sources: cli.EnvVars("MISE_GITHUB_TOKEN_PRESENT"), Usage: "whether the caller has a GitHub rate-limit token; pass presence only, never the token"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return apptoolchain.ValidateMiseInstall(apptoolchain.ValidateMiseInstallInput{
				Root:               cmd.String(flagMiseRoot),
				Locked:             cmd.String(flagLocked),
				GithubTokenPresent: cmd.Bool("github-token-present"),
			})
		},
	}
}
