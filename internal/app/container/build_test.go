// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

// fakeBuilder records the request it was handed for either build path.
type fakeBuilder struct {
	got         container.BuildRequest
	builtLayout bool
	calls       int
	layout      string
}

func (f *fakeBuilder) Build(_ context.Context, req container.BuildRequest, _ io.Writer) error {
	f.got = req
	f.calls++

	return nil
}

func (f *fakeBuilder) BuildToLayout(_ context.Context, req container.BuildRequest, directory string, _ io.Writer) error {
	f.got = req
	f.layout = directory
	f.builtLayout = true
	f.calls++

	return nil
}

// fakePusher records the ref and returns a canned digest.
type fakePusher struct {
	gotRef     string
	digest     string
	calls      int
	layout     string
	beforePush func()
}

func (f *fakePusher) PushLayoutByDigest(_ context.Context, directory, imageRef string) (string, error) {
	if f.beforePush != nil {
		f.beforePush()
	}

	f.gotRef = imageRef
	f.layout = directory
	f.calls++

	return f.digest, nil
}

// TestBuildImage_PushByDigestHandsTheBuilderEveryInput covers push-by-digest:
// the build goes through a layout, the pushed digest is what comes back and
// what is published as an output, and the request the builder receives carries
// every input it was given.
//
// The request is compared whole. It was checked five fields at a time with a
// condition that reported the whole struct, and the fields it did not name --
// build arguments and labels among them -- could have been dropped without
// failing anything, despite being the flags in the old name.
func TestBuildImage_PushByDigestHandsTheBuilderEveryInput(t *testing.T) {
	t.Parallel()

	builder := &fakeBuilder{}
	pusher := &fakePusher{digest: oneDigest}
	sink := fakeoutputsink.New(t)

	in := appcontainer.BuildImageInput{
		AuthFile:        "explicit-auth.json",
		Context:         ".",
		Containerfile:   "Containerfile",
		Target:          "build",
		Platform:        "linux/arm64",
		HostArch:        "arm64",
		BuildArgs:       []string{"VERSION=1.2.3"},
		Secrets:         []string{"id=db,src=/run/secrets/db"},
		Labels:          []string{"org.opencontainers.image.version=1.2.3"},
		SourceDateEpoch: "1700000000",
		CacheRepo:       "ghcr.io/org/buildcache",
		CachePush:       true,
		Mode:            container.BuildModePushByDigest,
		ImageRef:        "ghcr.io/org/repo",
	}

	got, err := appcontainer.BuildImage(context.Background(), builder, pusher, sink, io.Discard, in)
	if err != nil {
		t.Fatal(err)
	}

	if got != oneDigest {
		t.Errorf("returned digest = %q, want the digest the push reported", got)
	}

	if v := sink.Single("digest"); v != oneDigest {
		t.Errorf("sink digest = %q, want %s", v, oneDigest)
	}

	// Push-by-digest builds to an OCI layout and pushes that, rather than
	// building straight to a tag.
	if !builder.builtLayout {
		t.Error("push-by-digest must go through BuildToLayout")
	}

	if pusher.gotRef != "ghcr.io/org/repo" {
		t.Errorf("pushed to %q, want ghcr.io/org/repo", pusher.gotRef)
	}

	wantRequest := container.BuildRequest{
		AuthFile:        "explicit-auth.json",
		Context:         ".",
		Containerfile:   "Containerfile",
		Target:          "build",
		Platform:        "linux/arm64",
		BuildArgs:       []string{"VERSION=1.2.3"},
		Secrets:         []string{"id=db,src=/run/secrets/db"},
		Labels:          []string{"org.opencontainers.image.version=1.2.3"},
		SourceDateEpoch: "1700000000",
		CacheRepo:       "ghcr.io/org/buildcache",
		CachePush:       true,
		Mode:            container.BuildModePushByDigest,
		ImageRef:        "ghcr.io/org/repo",
	}
	if !reflect.DeepEqual(builder.got, wantRequest) {
		t.Errorf("build request =\n%+v\nwant\n%+v", builder.got, wantRequest)
	}
}

// TestBuildImage_InPlaceModesPushNothing covers the two modes that build in
// place. Neither may reach the pusher or publish an output: the image stays in
// local storage or a directory, and a digest output would name nothing.
func TestBuildImage_InPlaceModesPushNothing(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]appcontainer.BuildImageInput{
		"load":  {Context: ".", Mode: container.BuildModeLoad, ImageRef: "local:verify"},
		"local": {Context: ".", Mode: container.BuildModeLocal, OutputDir: "out"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			builder := &fakeBuilder{}
			pusher := &fakePusher{digest: oneDigest}
			sink := fakeoutputsink.New(t)

			digest, err := appcontainer.BuildImage(context.Background(), builder, pusher, sink, io.Discard, in)
			if err != nil {
				t.Fatal(err)
			}

			if builder.calls != 1 || builder.builtLayout {
				t.Errorf("builder calls = %d, layout = %v; want one in-place build", builder.calls, builder.builtLayout)
			}

			if pusher.calls != 0 {
				t.Errorf("pusher calls = %d, want none", pusher.calls)
			}

			if digest != "" || len(sink.Keys()) != 0 {
				t.Errorf("digest = %q, outputs = %v; want neither", digest, sink.Keys())
			}
		})
	}
}

func TestBuildImage_RejectsInvalidRequestBeforeBuilding(t *testing.T) {
	t.Parallel()

	builder := &fakeBuilder{}
	pusher := &fakePusher{digest: oneDigest}
	sink := fakeoutputsink.New(t)

	// load mode without an image ref is a usage error caught before the builder runs.
	_, err := appcontainer.BuildImage(context.Background(), builder, pusher, sink, io.Discard,
		appcontainer.BuildImageInput{Context: ".", Mode: container.BuildModeLoad})
	if !errors.Is(err, errs.ErrUsage) {
		t.Errorf("err = %v, want ErrUsage for load mode without image-ref", err)
	}

	if builder.calls != 0 || pusher.calls != 0 || len(sink.Keys()) != 0 {
		t.Errorf("builder calls = %d, pusher calls = %d, outputs = %v; want nothing touched", builder.calls, pusher.calls, sink.Keys())
	}
}

// A platform of another architecture than the host's would build under CPU
// emulation. Every mode refuses it before the builder or pusher runs, and an
// empty platform (Buildah's host default) remains a native build.
func TestBuildImage_RefusesAForeignPlatform(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]appcontainer.BuildImageInput{
		"push-by-digest": {Context: ".", Platform: "linux/arm64", HostArch: "amd64", Mode: container.BuildModePushByDigest, ImageRef: "ghcr.io/org/repo"},
		"load":           {Context: ".", Platform: "linux/amd64", HostArch: "arm64", Mode: container.BuildModeLoad, ImageRef: "local:verify"},
		"unknown host":   {Context: ".", Platform: "linux/amd64", Mode: container.BuildModeLoad, ImageRef: "local:verify"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			builder := &fakeBuilder{}
			pusher := &fakePusher{digest: oneDigest}
			sink := fakeoutputsink.New(t)

			_, err := appcontainer.BuildImage(context.Background(), builder, pusher, sink, io.Discard, in)
			if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "runner of that architecture") {
				t.Errorf("err = %v, want a validation refusal naming the native-runner remedy", err)
			}

			if builder.calls != 0 || pusher.calls != 0 || len(sink.Keys()) != 0 {
				t.Errorf("builder calls = %d, pusher calls = %d, outputs = %v; want nothing touched", builder.calls, pusher.calls, sink.Keys())
			}
		})
	}
}
