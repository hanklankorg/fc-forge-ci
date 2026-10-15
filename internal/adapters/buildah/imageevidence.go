// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah

import (
	"context"
	"fmt"
	"io"
)

// ExportLocalImageToLayout exports an image from buildah's local storage to an OCI layout.
func (a *Adapter) ExportLocalImageToLayout(ctx context.Context, imageRef, layoutDir string, out io.Writer) error {
	return a.run(ctx, out, a.global("push", imageRef, "oci:"+layoutDir+":scan")...)
}

// ExportImageToLayout keeps the caller's explicit registry policy during export.
// The destination is local OCI storage, never a docker:// registry write.
func (a *Adapter) ExportImageToLayout(ctx context.Context, authFile string, tlsVerify bool, image, layoutDir string, out io.Writer) error {
	args := append(a.global("push"), exportAuthArgs(authFile, tlsVerify)...)
	args = append(args, image, "oci:"+layoutDir+":scan")

	return a.runWithEnv(ctx, out, registryAuthEnv(authFile), args...)
}

// ExportManifestToLayout includes all platforms; remote children, if needed,
// use the same auth file and TLS policy as the subsequent registry publication.
func (a *Adapter) ExportManifestToLayout(ctx context.Context, authFile string, tlsVerify bool, manifest, layoutDir string, out io.Writer) error {
	args := append(a.global("manifest", "push", "--all"), exportAuthArgs(authFile, tlsVerify)...)
	args = append(args, manifest, "oci:"+layoutDir+":scan")

	return a.runWithEnv(ctx, out, registryAuthEnv(authFile), args...)
}

// RemoveLocalManifest runs only after the caller has verified publication.
func (a *Adapter) RemoveLocalManifest(ctx context.Context, manifest string, out io.Writer) error {
	return a.run(ctx, out, a.global("manifest", "rm", manifest)...)
}

// ExportLocalManifestToLayout exports a local Buildah manifest list to an OCI layout.
func (a *Adapter) ExportLocalManifestToLayout(ctx context.Context, manifest, layoutDir string, out io.Writer) error {
	return a.run(ctx, out, a.global("manifest", "push", "--all", manifest, "oci:"+layoutDir+":scan")...)
}

func exportAuthArgs(authFile string, tlsVerify bool) []string {
	args := []string{fmt.Sprintf("--tls-verify=%t", tlsVerify)}
	if authFile != "" {
		args = append(args, "--authfile", authFile)
	}

	return args
}
