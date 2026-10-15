// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

var errPublicationFixture = errors.New("inert publication failure")

type fakeManifestPushTool struct {
	exports       []string
	layout        string
	tlsVerify     bool
	removed       []string
	failure       error
	removeFailure error
	beforeRemove  func()
}

func (f *fakeManifestPushTool) ExportManifestToLayout(_ context.Context, auth string, verify bool, manifest, directory string, _ io.Writer) error {
	f.exports = append(f.exports, auth+"|"+manifest)
	f.layout, f.tlsVerify = directory, verify

	return f.failure
}

func (f *fakeManifestPushTool) RemoveLocalManifest(_ context.Context, manifest string, _ io.Writer) error {
	if f.beforeRemove != nil {
		f.beforeRemove()
	}

	f.removed = append(f.removed, manifest)

	return f.removeFailure
}

type fakeManifestPushRegistry struct {
	raw           []byte
	failures      int
	queried       []string
	pushFailures  int
	digest        string
	invalidDigest bool
	pushes        []string
	layouts       []string
	onPush        func(string, string)
}

func (f *fakeManifestPushRegistry) Manifest(_ context.Context, ref string) ([]byte, error) {
	f.queried = append(f.queried, ref)
	if f.failures > 0 {
		f.failures--

		return nil, errPublicationFixture
	}

	return f.raw, nil
}

func (f *fakeManifestPushRegistry) PushLayout(_ context.Context, directory, destination string) (string, error) {
	return f.push(directory, destination)
}

func (f *fakeManifestPushRegistry) PushIndexLayout(_ context.Context, directory, destination string) (string, error) {
	return f.push(directory, destination)
}

func (f *fakeManifestPushRegistry) push(directory, destination string) (string, error) {
	f.pushes, f.layouts = append(f.pushes, destination), append(f.layouts, directory)
	if f.onPush != nil {
		f.onPush(directory, destination)
	}

	if f.pushFailures > 0 {
		f.pushFailures--

		return "", errPublicationFixture
	}

	if f.invalidDigest || f.digest != "" {
		return f.digest, nil
	}

	return manifestTestDigest(f.raw), nil
}

func TestPushManifest_RetriesOneExportAndRemovesOnlyAfterVerification(t *testing.T) {
	t.Parallel()

	tool := &fakeManifestPushTool{}
	registry := &fakeManifestPushRegistry{raw: []byte(`{"schemaVersion":2}`), pushFailures: 1, failures: 1}
	tool.beforeRemove = func() {
		if len(registry.queried) != 2 {
			t.Error("local manifest removed before successful read-back")
		}
	}
	sink := fakeoutputsink.New(t)

	result, err := appcontainer.PushManifest(t.Context(), tool, registry, sink, io.Discard, appcontainer.PushManifestInput{
		LocalManifest: "localhost/app:list", Destination: "registry.example/app:staging", AuthFile: "auth.json", TLSVerify: "false", RemoveLocal: true,
		RetryAttempts: 2, RetryDelay: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	digest := manifestTestDigest(registry.raw)
	if result.Digest != digest || result.Ref != "registry.example/app:staging@"+digest || sink.Single("digest") != digest || sink.Single("ref") != result.Ref {
		t.Fatalf("metadata: %+v", result)
	}

	if !slices.Equal(tool.exports, []string{"auth.json|localhost/app:list"}) || tool.tlsVerify || !slices.Equal(tool.removed, []string{"localhost/app:list"}) {
		t.Fatalf("export/cleanup: %+v", tool)
	}

	if !slices.Equal(registry.layouts, []string{tool.layout, tool.layout}) || !slices.Equal(registry.pushes, registry.queried) {
		t.Fatalf("retry changed the source/destination: %+v", registry)
	}

	assertPublicationLayoutRemoved(t, tool.layout)
}

func TestPushManifest_FailureRetainsLocalStateAndEmitsNothing(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"export", "push", "read", "mismatch", "missing digest", "remove"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			tool := &fakeManifestPushTool{}
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
			case "remove":
				tool.removeFailure = os.ErrPermission
			}

			sink := fakeoutputsink.New(t)

			result, err := appcontainer.PushManifest(t.Context(), tool, registry, sink, io.Discard, appcontainer.PushManifestInput{
				LocalManifest: "localhost/app:list", Destination: "registry.example/app:v1", TLSVerify: "true", RemoveLocal: true, RetryAttempts: 1,
			})
			if err == nil || result != nil || len(sink.Keys()) != 0 {
				t.Fatalf("failure emitted metadata: %+v %v", result, err)
			}

			if mode != "remove" && len(tool.removed) != 0 {
				t.Fatal("unverified publication removed local state")
			}

			if mode == "export" && len(registry.pushes) != 0 {
				t.Fatal("failed export published")
			}

			if mode == "missing digest" && (!errors.Is(err, errs.ErrInvalidConfig) || len(registry.queried) != 0) {
				t.Fatal("invalid publisher digest fell back to registry")
			}

			assertPublicationLayoutRemoved(t, tool.layout)
		})
	}
}

func TestPushManifest_InputRefusals(t *testing.T) {
	t.Parallel()

	valid := appcontainer.PushManifestInput{LocalManifest: "localhost/app:list", Destination: "registry.example/app:v1", TLSVerify: "true", RetryAttempts: 1}
	for _, mutate := range []func(*appcontainer.PushManifestInput){
		func(in *appcontainer.PushManifestInput) { in.LocalManifest = "" },
		func(in *appcontainer.PushManifestInput) { in.LocalManifest = "bad\nref" },
		func(in *appcontainer.PushManifestInput) { in.Destination = "" },
		func(in *appcontainer.PushManifestInput) { in.Destination += " --flag" },
		func(in *appcontainer.PushManifestInput) { in.TLSVerify = "" },
		func(in *appcontainer.PushManifestInput) { in.TLSVerify = "TRUE" },
	} {
		in := valid
		mutate(&in)

		tool, registry := &fakeManifestPushTool{}, &fakeManifestPushRegistry{}

		_, err := appcontainer.PushManifest(t.Context(), tool, registry, nil, io.Discard, in)
		if !errors.Is(err, errs.ErrUsage) || len(tool.exports) != 0 || len(registry.pushes) != 0 || len(tool.removed) != 0 {
			t.Fatalf("refusal had effects: %v", err)
		}
	}

	if _, err := appcontainer.PushManifest(t.Context(), nil, &fakeManifestPushRegistry{}, nil, io.Discard, valid); !errors.Is(err, errs.ErrUsage) {
		t.Fatal(err)
	}

	if _, err := appcontainer.PushManifest(t.Context(), &fakeManifestPushTool{}, nil, nil, io.Discard, valid); !errors.Is(err, errs.ErrUsage) {
		t.Fatal(err)
	}
}

func TestResolvePushedManifestDigest_OptionalLegacyFileAndRetries(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"schemaVersion":2}`)

	digest := manifestTestDigest(raw)
	for _, value := range []string{"", digest + "\n", "not-a-digest", "sha256:" + strings.Repeat("0", 64)} {
		input := appcontainer.PushedManifestDigestInput{Ref: "registry.example/app:v1", RetryAttempts: 2, RetryDelay: time.Nanosecond}
		if value != "" {
			input.DigestFile = filepath.Join(t.TempDir(), "digest")
			if err := os.WriteFile(input.DigestFile, []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		registry := &fakeManifestPushRegistry{raw: raw, failures: 1}
		sink := fakeoutputsink.New(t)

		var log bytes.Buffer

		result, err := appcontainer.ResolvePushedManifestDigest(t.Context(), registry, sink, &log, input)
		if strings.HasSuffix(value, strings.Repeat("0", 64)) {
			if !errors.Is(err, errs.ErrValidation) || result != nil || len(sink.Keys()) != 0 {
				t.Fatal("mismatched legacy digest accepted")
			}

			continue
		}

		if err != nil || result.Digest != digest || sink.Single("ref") != "registry.example/app:v1@"+digest {
			t.Fatalf("legacy lookup: %+v %v", result, err)
		}

		if !slices.Equal(registry.queried, []string{input.Ref, input.Ref}) {
			t.Fatal("lookup retried another reference")
		}

		if strings.Contains(log.String(), "digest file was empty or invalid") != (value == "not-a-digest") {
			t.Fatal("legacy digest warning changed")
		}
	}
}

func TestResolvePushedManifestDigest_InputRefusals(t *testing.T) {
	t.Parallel()

	if _, err := appcontainer.ResolvePushedManifestDigest(t.Context(), nil, nil, io.Discard, appcontainer.PushedManifestDigestInput{Ref: "registry.example/app:v1"}); !errors.Is(err, errs.ErrUsage) {
		t.Fatal(err)
	}

	for _, ref := range []string{"", " ", "registry.example/app:v1 extra", "registry.example/app:v1\nflag"} {
		registry := &fakeManifestPushRegistry{}

		_, err := appcontainer.ResolvePushedManifestDigest(t.Context(), registry, nil, io.Discard, appcontainer.PushedManifestDigestInput{Ref: ref, RetryAttempts: 1})
		if !errors.Is(err, errs.ErrUsage) || len(registry.queried) != 0 {
			t.Fatalf("invalid lookup had effects: %v", err)
		}
	}
}

func manifestTestDigest(raw []byte) string {
	sum := sha256.Sum256(raw)

	return "sha256:" + hex.EncodeToString(sum[:])
}
