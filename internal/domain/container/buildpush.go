// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

// BuildPushPlatformBuild describes one platform entry in the build-push-oci-image action's JSON contract.
type BuildPushPlatformBuild struct {
	Platform  string   `json:"platform"`
	BuildArgs []string `json:"build-args"`
}

// BuildPushManifestBuildRequest is the buildah-level request for one platform build into a shared manifest list.
type BuildPushManifestBuildRequest struct {
	AuthFile        string
	Manifest        string
	TLSVerify       bool
	SourceDateEpoch string
	Platform        string
	Containerfile   string
	BuildArgs       []string
	Labels          []string
	Context         string
}

// The registry transport of an image build is retried: a build fails on
// its base image when the resolver stops answering for a couple of minutes,
// and the builders' defaults give up sooner (Buildah after three retries one,
// two and four seconds apart, kaniko at once). Buildah takes both values, as
// --retry and --retry-delay, for the pulls and pushes of a build; kaniko
// takes the count, as --image-download-retry, for fetching a base image, and
// waits one second doubled each time. Neither retries a build step.
const (
	RegistryRetries    = 5
	RegistryRetryDelay = "15s"
)
