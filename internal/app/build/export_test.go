// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"context"
	"io"

	domainbuild "github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/output"
)

// The release builds call these steps directly and nothing outside this package
// should. The forwarders below let the black-box tests in this directory hold
// each step to its own contract without widening the API. They carry no
// behaviour: every one hands its arguments straight to the step it names.

// ResolveCargoMetadataForTest returns the resolved binary name, version and
// package name, in that order.
func ResolveCargoMetadataForTest(ctx context.Context, tool CargoTool, in CargoMetadataInput) (string, string, string, error) {
	meta, err := resolveCargoMetadata(ctx, tool, in)

	return meta.binaryName, meta.version, meta.packageName, err
}

func ResolveGradleVersionForTest(dir string) string {
	return resolveGradleVersion(dir)
}

func DecodeXcodeXCConfigForTest(encoded string) ([]byte, error) {
	return decodeXcodeXCConfig(encoded)
}

func WriteXcodeXCConfigForTest(body []byte, tempDir string) (string, error) {
	return writeXcodeXCConfig(body, tempDir)
}

func ResolveXcodeVersionInfoForTest(in XcodeVersionInfoInput) (domainbuild.XcodeVersionInfo, bool, error) {
	return resolveXcodeVersionInfo(in)
}

func ReportXcodeVersionInfoForTest(ctx context.Context, sink ci.OutputSink, stderr io.Writer, annot output.Annotator, got domainbuild.XcodeVersionInfo, missing bool) error {
	return reportXcodeVersionInfo(ctx, sink, stderr, annot, got, missing)
}

func DecodeXcodeSigningSecretsForTest(in XcodeSetupCodeSigningInput) ([]byte, []byte, error) {
	return decodeXcodeSigningSecrets(in)
}

// SetupXcodeSigningForTest drops the returned state: it is an unexported type,
// and the tests observe the setup through the recorded security calls and the
// files it leaves.
func SetupXcodeSigningForTest(ctx context.Context, sec XcodeSecurityOps, out io.Writer, in XcodeSetupCodeSigningInput, cert, profile []byte) error {
	_, err := setupXcodeSigning(ctx, sec, out, in, cert, profile)

	return err
}
