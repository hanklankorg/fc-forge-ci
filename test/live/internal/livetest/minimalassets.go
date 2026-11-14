// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	errEmbeddedInputOutsideRoot = errors.New("embedded CLI input is outside the snapshot root")
)

// minimalEmbeddedAssets asks the compiler for the CLI's actual embedded inputs;
// an extension allowlist misses assets such as administrator-owned policy.json.
func minimalEmbeddedAssets(ctx context.Context, root string) ([]string, error) {
	command := exec.CommandContext(ctx, "go", "list", "-deps", "-f",
		`{{if .Module}}{{if .Module.Main}}{{range .EmbedFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}{{end}}{{end}}`, "./cmd/forge-ci")
	command.Dir = root

	command.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GONOPROXY=none", "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=readonly")

	body, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("resolve embedded CLI source inputs: %w", err)
	}

	var paths []string

	for file := range strings.SplitSeq(string(body), "\n") {
		if file == "" {
			continue
		}

		relative, err := filepath.Rel(root, file)
		if err != nil || !filepath.IsLocal(relative) {
			return nil, errEmbeddedInputOutsideRoot
		}

		paths = append(paths, relative)
	}

	return paths, nil
}
