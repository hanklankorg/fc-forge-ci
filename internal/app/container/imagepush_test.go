// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

type fakeImagePushTool struct {
	exports   []string
	layout    string
	tlsVerify bool
	failure   error
}

func (f *fakeImagePushTool) ExportImageToLayout(_ context.Context, auth string, verify bool, image, directory string, _ io.Writer) error {
	f.exports = append(f.exports, auth+"|"+image)
	f.layout, f.tlsVerify = directory, verify

	return f.failure
}

func TestPushImage_ExportsOnceAndVerifiesEveryRetry(t *testing.T) {
	t.Parallel()

	tool := &fakeImagePushTool{}
	registry := &fakeManifestPushRegistry{raw: []byte(`{"schemaVersion":2}`), pushFailures: 1, failures: 1}
	sink := fakeoutputsink.New(t)

	var log bytes.Buffer

	result, err := appcontainer.PushImage(t.Context(), tool, registry, sink, &log, appcontainer.PushImageInput{
		LocalImage: "localhost/app:arch", Destination: "registry.example/app:staging", AuthFile: "auth.json", TLSVerify: "true",
		RetryAttempts: 2, RetryDelay: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	digest := manifestTestDigest(registry.raw)
	if result.Digest != digest || result.Ref != "registry.example/app:staging@"+digest || sink.Single("digest") != digest || sink.Single("ref") != result.Ref {
		t.Fatalf("incorrect publication metadata: %+v", result)
	}

	if !slices.Equal(tool.exports, []string{"auth.json|localhost/app:arch"}) || !tool.tlsVerify {
		t.Fatalf("export inputs: %v TLS=%v", tool.exports, tool.tlsVerify)
	}

	if !slices.Equal(registry.pushes, []string{"registry.example/app:staging", "registry.example/app:staging"}) ||
		!slices.Equal(registry.layouts, []string{tool.layout, tool.layout}) || !slices.Equal(registry.queried, registry.pushes) {
		t.Fatalf("publication changed source or destination: %+v", registry)
	}

	if !strings.Contains(log.String(), "attempt 1/2") {
		t.Fatal("retry was not reported")
	}

	assertPublicationLayoutRemoved(t, tool.layout)
}

func TestPushImage_FailuresPublishNoOutputs(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"export", "push", "read", "mismatch", "missing digest"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			tool := &fakeImagePushTool{}
			registry := &fakeManifestPushRegistry{raw: []byte(`{"schemaVersion":2}`)}

			switch mode {
			case "export":
				tool.failure = os.ErrPermission
			case "push":
				registry.pushFailures = 1
			case "read":
				registry.failures = 1
			case "mismatch":
				registry.digest = "sha256:" + strings.Repeat("0", 64)
			case "missing digest":
				registry.invalidDigest = true
			}

			sink := fakeoutputsink.New(t)

			result, err := appcontainer.PushImage(t.Context(), tool, registry, sink, io.Discard, appcontainer.PushImageInput{
				LocalImage: "localhost/app:arch", Destination: "registry.example/app:v1", TLSVerify: "true", RetryAttempts: 1,
			})
			if err == nil || result != nil || len(sink.Keys()) != 0 {
				t.Fatalf("failed publication emitted metadata: %+v %v", result, err)
			}

			if mode == "export" && len(registry.pushes) != 0 {
				t.Fatal("failed export reached publication")
			}

			if mode == "missing digest" && (!errors.Is(err, errs.ErrInvalidConfig) || len(registry.queried) != 0) {
				t.Fatal("missing expected digest was treated as optional")
			}

			assertPublicationLayoutRemoved(t, tool.layout)
		})
	}
}

func TestPushImage_InputRefusals(t *testing.T) {
	t.Parallel()

	for _, mutation := range []func(*appcontainer.PushImageInput){
		func(in *appcontainer.PushImageInput) { in.LocalImage = "" },
		func(in *appcontainer.PushImageInput) { in.LocalImage = "bad ref" },
		func(in *appcontainer.PushImageInput) { in.Destination = "" },
		func(in *appcontainer.PushImageInput) { in.Destination = "registry.example/app:v1\nflag" },
		func(in *appcontainer.PushImageInput) { in.TLSVerify = "" },
		func(in *appcontainer.PushImageInput) { in.TLSVerify = "yes" },
	} {
		in := appcontainer.PushImageInput{LocalImage: "localhost/app:arch", Destination: "registry.example/app:v1", TLSVerify: "true", RetryAttempts: 1}
		mutation(&in)

		tool, registry := &fakeImagePushTool{}, &fakeManifestPushRegistry{}

		_, err := appcontainer.PushImage(t.Context(), tool, registry, nil, io.Discard, in)
		if !errors.Is(err, errs.ErrUsage) || len(tool.exports) != 0 || len(registry.pushes) != 0 {
			t.Fatalf("refusal had effects: %v", err)
		}
	}

	valid := appcontainer.PushImageInput{LocalImage: "localhost/app:arch", Destination: "registry.example/app:v1", TLSVerify: "true"}
	if _, err := appcontainer.PushImage(t.Context(), nil, &fakeManifestPushRegistry{}, nil, io.Discard, valid); !errors.Is(err, errs.ErrUsage) {
		t.Fatal(err)
	}

	if _, err := appcontainer.PushImage(t.Context(), &fakeImagePushTool{}, nil, nil, io.Discard, valid); !errors.Is(err, errs.ErrUsage) {
		t.Fatal(err)
	}
}

func assertPublicationLayoutRemoved(t *testing.T, directory string) {
	t.Helper()

	if directory == "" {
		t.Fatal("no publication layout was created")
	}

	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary layout retained: %v", err)
	}
}
