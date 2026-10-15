// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah

import (
	"context"
	"fmt"
	"io"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

// BuildManifest builds one platform into a shared buildah manifest list.
func (a *Adapter) BuildManifest(ctx context.Context, req container.BuildPushManifestBuildRequest, out io.Writer) error {
	args := append(a.global("bud", "--pull=always"), chrootIsolation()...)
	args = append(args,
		"--platform", req.Platform,
		fmt.Sprintf("--tls-verify=%t", req.TLSVerify),
		"--timestamp", req.SourceDateEpoch,
		"--manifest", req.Manifest,
	)

	for _, label := range req.Labels {
		args = append(args, "--label", label)
	}

	args = append(args, "-f", req.Containerfile)
	for _, buildArg := range req.BuildArgs {
		args = append(args, "--build-arg", buildArg)
	}

	args = append(args, req.Context)

	return a.runWithEnv(ctx, out, registryAuthEnv(req.AuthFile), args...)
}

func registryAuthEnv(authFile string) []string {
	if authFile == "" {
		return nil
	}

	return []string{"REGISTRY_AUTH_FILE=" + authFile}
}
