// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func TestMobileSecretBoundary_DecodedRefusalsHaveNoEffects(t *testing.T) {
	for _, encoded := range []string{" \r\n\t", "%%%", strings.Repeat("YQ==", 9<<20)} {
		fsys := xcodeExportFixture(t)
		before := ownedTree(t, fsys.Root)

		var out bytes.Buffer

		sec := &fakeSecurity{}
		good := base64.StdEncoding.EncodeToString([]byte("valid fake material"))

		calls := []func() error{
			func() error {
				return setupXcodeCodeSigning(t.Context(), sec, &out, appbuild.XcodeSetupCodeSigningInput{CertBase64: good, PPBase64: encoded, TempDir: fsys.Root, ProvisioningProfilesDir: fsys.Root})
			},
		}
		for _, call := range calls {
			if err := call(); !errors.Is(err, errs.ErrMalformedInput) {
				t.Fatalf("err=%v", err)
			}

			if len(sec.calls) != 0 || out.Len() != 0 || !reflect.DeepEqual(before, ownedTree(t, fsys.Root)) {
				t.Fatal("invalid secret caused effects")
			}
		}
	}
}

func TestAndroidArtifactBoundary_RejectsLinksAndWrongTypes(t *testing.T) {
	for _, kind := range []string{"valid", "leaf link", "parent link", "directory app"} {
		t.Run(kind, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.Chdir()

			const directory, ext = "app/build/outputs", ".apk"

			fsys.MkdirAll(directory)
			fsys.WriteFile(directory+"/a-good"+ext, []byte("application"))
			canary := fsys.WriteFile("outside-canary", []byte("outside bytes"))

			switch kind {
			case "leaf link":
				if err := os.Symlink(canary, filepath.Join(fsys.Root, directory, "z-bad"+ext)); err != nil {
					t.Fatal(err)
				}
			case "parent link":
				if err := os.Symlink(fsys.Root, filepath.Join(fsys.Root, directory, "nested")); err != nil {
					t.Fatal(err)
				}
			case "directory app":
				fsys.MkdirAll(directory, "z-bad"+ext)
			}

			before := ownedTree(t, fsys.Root)

			var out bytes.Buffer

			err := appbuild.AndroidListArtifacts(&out, appbuild.AndroidListArtifactsInput{Root: fsys.Root, BuildModule: "app"})

			if kind == "valid" {
				if err != nil || !strings.Contains(out.String(), "a-good"+ext) {
					t.Fatalf("err=%v output=%s", err, &out)
				}
			} else if !errors.Is(err, errs.ErrValidation) || out.Len() != 0 {
				t.Fatalf("err=%v output=%s", err, &out)
			}

			if !reflect.DeepEqual(before, ownedTree(t, fsys.Root)) {
				t.Fatal("discovery changed tree")
			}
		})
	}
}

func TestXcodeIdentityBoundary_RefusesBeforeEffects(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.MkdirAll("App.xcodeproj")
	fsys.MkdirAll("App.xcworkspace")
	fsys.WriteFile("File.xcodeproj", []byte("not a project directory"))

	for _, pair := range [][2]string{{"", ""}, {"App.xcworkspace", "App.xcodeproj"}, {"", "missing.xcodeproj"}, {"", "File.xcodeproj"}, {"   ", " "}} {
		before := ownedTree(t, fsys.Root)
		ops := &fakeXcodeBuild{}

		var out bytes.Buffer

		err := appbuild.XcodeReleaseBuild(t.Context(), fakeoutputsink.New(t), unsignedXcodeSecurity{t}, ops, output.Annotator{}, &out, &out,
			appbuild.XcodeReleaseBuildInput{RepositoryName: "app", Workspace: pair[0], Project: pair[1], Scheme: "Scheme", Configuration: "Release", Destination: "generic/platform=iOS"})
		if err == nil || len(ops.calls) != 0 || out.Len() != 0 || !reflect.DeepEqual(before, ownedTree(t, fsys.Root)) {
			t.Fatalf("pair=%v err=%v calls=%v output=%s", pair, err, ops.calls, &out)
		}
	}
}
