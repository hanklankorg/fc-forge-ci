// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package toolchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// CacheDiscriminatorInput drives `toolchain cache-discriminator`.
type CacheDiscriminatorInput struct {
	Tools           string
	InstallDevTools string
	ExtraCachePaths string
}

// CacheDiscriminator returns the short stable cache-key discriminator used by
// setup-toolchain for selected tool subsets and extra cache paths.
func CacheDiscriminator(in CacheDiscriminatorInput) string {
	hash := sha256.New()
	for _, value := range []string{in.Tools, in.InstallDevTools, in.ExtraCachePaths} {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = io.WriteString(hash, value)
	}

	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// Go providers of a setup-toolchain job, as `toolchain setup-go-env` names them.
const (
	// GoProviderMise: the consumer's mise config declares Go, so the mise
	// install brings it, from mise.lock in locked mode.
	GoProviderMise = "mise"
	// GoProviderGoMod: mise installs no Go, so the action installs the one
	// go.mod names.
	GoProviderGoMod = "go.mod"
)

// SetupGoEnvInput drives `toolchain setup-go-env`.
type SetupGoEnvInput struct {
	Root     string
	PathFile string
	EnvFile  string
}

// SetupGoEnv says which of setup-toolchain's two Go sources installs Go for
// the repository at Root, so that a job downloads Go once. When mise does, it
// writes the environment the action's setup-go step would have written beside
// its own install: GOTOOLCHAIN=local, so that the go command never fetches
// another toolchain, and GOPATH/bin on PATH.
func SetupGoEnv(in SetupGoEnvInput) (string, error) {
	if in.PathFile == "" {
		return "", fmt.Errorf("setup-go-env: path-file is required: %w", errs.ErrUsage)
	}

	if in.EnvFile == "" {
		return "", fmt.Errorf("setup-go-env: env-file is required: %w", errs.ErrUsage)
	}

	root := in.Root
	if root == "" {
		root = "."
	}

	configs, _, err := readMiseConfigs(root)
	if err != nil {
		return "", err
	}

	if !toolDeclared(configs, toolGo) {
		return GoProviderGoMod, nil
	}

	goBin, err := goPathBin()
	if err != nil {
		return "", err
	}

	if err = appendPathFile(in.PathFile, goBin); err != nil {
		return "", err
	}

	if err = appendEnvFile(in.EnvFile, "GOTOOLCHAIN=local"); err != nil {
		return "", err
	}

	return GoProviderMise, nil
}

// goPathBin is where `go install` writes: the bin directory of the first
// GOPATH entry, and of $HOME/go without one.
func goPathBin() (string, error) {
	if entries := filepath.SplitList(os.Getenv("GOPATH")); len(entries) > 0 && entries[0] != "" {
		return resolveMiseLocalPath(filepath.Join(entries[0], "bin"), true)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return resolveMiseLocalPath(filepath.Join(home, "go", "bin"), true)
}

// SetupMiseEnvInput drives `toolchain setup-mise-env`.
type SetupMiseEnvInput struct {
	Cache      string
	BinHome    string
	PathFile   string
	EnvFile    string
	RunnerTemp string
}

// SetupMiseEnv writes the runner path/env file entries needed after installing
// mise and, in cache=false mode, isolates mise's mutable data/cache/state trees
// under the runner temp directory.
// Runner export files are append-only: identical reruns append identical entries
// rather than rewriting exports left by an earlier command in the same step.
// Known local obstacles refuse before any creation or append. Later I/O failures
// may leave created directories or earlier appends; this is not a transaction.
func SetupMiseEnv(in SetupMiseEnvInput) error { //nolint:cyclop // plan both runner files and every directory before effects.
	cacheEnabled, err := parseSetupCache(in.Cache)
	if err != nil {
		return err
	}

	if in.PathFile == "" {
		return fmt.Errorf("setup-mise-env: path-file is required: %w", errs.ErrUsage)
	}

	if in.EnvFile == "" {
		return fmt.Errorf("setup-mise-env: env-file is required: %w", errs.ErrUsage)
	}

	binHome, err := resolveBinHome(in.BinHome)
	if err != nil {
		return err
	}

	binHome, err = resolveMiseLocalPath(binHome, true)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}

	home, err = resolveMiseLocalPath(home, true)
	if err != nil {
		return err
	}

	temp, err := defaultRunnerTemp(in.RunnerTemp)
	if err != nil {
		return err
	}

	temp, err = resolveMiseLocalPath(temp, false)
	if err != nil {
		return err
	}

	configDir := filepath.Join(temp, "setup-toolchain-mise-config")
	dirs := []string{binHome, filepath.Join(home, ".local", "share", "mise"), configDir}
	entries := []string{"MISE_CONFIG_DIR=" + configDir}

	if !cacheEnabled {
		base := filepath.Join(temp, "setup-toolchain-mise-data")
		for _, name := range []string{"data", "cache", "state"} {
			dirs = append(dirs, filepath.Join(base, name))
		}

		entries = append(entries,
			"MISE_DATA_DIR="+filepath.Join(base, "data"),
			"MISE_CACHE_DIR="+filepath.Join(base, "cache"),
			"MISE_STATE_DIR="+filepath.Join(base, "state"),
		)
	}

	pathFile, err := resolveMiseLocalPath(in.PathFile, false)
	if err != nil {
		return err
	}

	envFile, err := resolveMiseLocalPath(in.EnvFile, false)
	if err != nil {
		return err
	}

	if err = preflightMiseLocalPaths(dirs, []string{pathFile, envFile}); err != nil {
		return err
	}

	for _, dir := range dirs {
		root, createErr := pathsafe.MkdirRoot(dir, 0o755)
		if createErr != nil {
			return fmt.Errorf("create mise directory %s: %w", dir, createErr)
		}

		_ = root.Close()
	}

	if err = appendPathFile(pathFile, binHome); err != nil {
		return err
	}

	if err = appendPathFile(pathFile, filepath.Join(home, ".cargo", "bin")); err != nil {
		return err
	}

	return appendEnvFile(envFile, entries...)
}

// PATH list separators only matter for exported paths, not ENV values or local
// output filenames. Validate raw spelling too, before Abs can erase components.
func resolveMiseLocalPath(path string, pathEntry bool) (string, error) {
	if strings.ContainsAny(path, "\x00\r\n\t") || (pathEntry && !validMisePathSpelling(path)) {
		return "", fmt.Errorf("invalid mise local path %q: %w", path, errs.ErrValidation)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	if strings.ContainsAny(abs, "\x00\r\n\t") || (pathEntry && !validMisePathSpelling(abs)) {
		return "", fmt.Errorf("invalid resolved mise local path %q: %w", abs, errs.ErrValidation)
	}

	return abs, nil
}

func misePathWithin(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// A missing runner-file parent is valid only if a planned MkdirAll creates it.
// OpenRoot checks every existing ancestor without following links, even when a
// later component is missing. This preflight performs no filesystem mutations.
func preflightMiseLocalPaths(dirs, files []string) error { //nolint:cyclop,gocognit // both path spelling and existing-file identity participate in the joint plan.
	for _, dir := range dirs {
		root, err := pathsafe.OpenRoot(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect planned mise directory %s: %w", dir, err)
		}

		if root != nil {
			_ = root.Close()
		}
	}

	infos := make([]os.FileInfo, len(files))
	for index, path := range files {
		parentPlanned := false

		for _, dir := range dirs {
			if misePathWithin(dir, path) {
				return fmt.Errorf("runner/install file overlaps planned directory %s: %w", path, errs.ErrValidation)
			}

			parentPlanned = parentPlanned || misePathWithin(dir, filepath.Dir(path))
		}

		root, err := pathsafe.OpenRoot(filepath.Dir(path))
		if err != nil && (!parentPlanned || !errors.Is(err, os.ErrNotExist)) {
			return fmt.Errorf("open runner/install parent %s: %w", path, err)
		}

		if root != nil {
			infos[index], err = root.Lstat(filepath.Base(path))
			_ = root.Close()

			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect runner/install file %s: %w", path, err)
			}

			if infos[index] != nil && !infos[index].Mode().IsRegular() {
				return fmt.Errorf("runner/install destination %s must be a nonlinked regular file: %w", path, errs.ErrValidation)
			}
		}

		for previous := range index {
			if path == files[previous] || (infos[index] != nil && infos[previous] != nil && os.SameFile(infos[index], infos[previous])) {
				return fmt.Errorf("runner/install files alias %s and %s: %w", files[previous], path, errs.ErrValidation)
			}
		}
	}

	return nil
}

func parseSetupCache(value string) (bool, error) {
	switch strings.TrimSpace(value) {
	case "", "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("setup-mise-env: cache must be true or false: %s: %w", value, errs.ErrUsage)
	}
}

// defaultRunnerTemp falls back to the OS temp dir when the run context
// carries no scratch directory.
//
// It does NOT consult $RUNNER_TEMP: value arrives from --temp-dir, whose
// sources already include it (via cienv.TempDir()) alongside $CI_TEMP_DIR.
// See defaultReleaseRunnerTemp in app/release for the same reasoning.
func defaultRunnerTemp(value string) (string, error) {
	if strings.TrimSpace(value) != "" {
		return value, nil
	}

	// OS temp locations may have trusted aliases such as /var -> /private/var.
	// Do not extend this exception to explicit caller-selected destinations.
	temp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("resolve OS temp directory: %w", err)
	}

	return temp, nil
}

func appendEnvFile(path string, entries ...string) error {
	if err := cliio.AppendLines(path, entries...); err != nil {
		return fmt.Errorf("append env entries: %w", err)
	}

	return nil
}

func printToolVersion(ctx context.Context, out io.Writer, bin string) error {
	cmd := safeexec.Command(ctx, bin, "--version")

	stdout, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run %s --version: %w", bin, safeexec.WrapError(err, bin, "--version"))
	}

	if out != nil && len(stdout) > 0 {
		_, _ = out.Write(stdout)
		if !strings.HasSuffix(string(stdout), "\n") {
			_, _ = fmt.Fprintln(out)
		}
	}

	return nil
}
