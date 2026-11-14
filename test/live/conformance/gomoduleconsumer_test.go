// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// A released Go module reaches a downstream module by its tag.
//
// The product publishes no Go package: docs/ecosystems.md offers a Go library
// as "tag = module version", for a go.mod whose module path matches the
// repository. So the release is LAYER-4's, in a repository of its own and held
// to every assertion of TestForgejoLayer_MinimalBinaryRelease, with two
// differences a library needs: go.mod declares the path Go fetches the
// repository by, and the module has a package to import. The binary is named
// in the build, because the product would otherwise name it after the module.
//
// A second repository then runs the downstream module's job. It has no
// credential, no proxy, no checksum database and an empty module cache; it
// asks the forge for the module at the final tag, and must get the release
// commit, which the host has already found the bot-signed tag pointing at, and
// run the library's code.
//
// Public and SHA-1 only. An anonymous `go get` is the subject, and what a
// private module needs is the consumer's Git credentials, not the product's.
func TestForgejoLayer_GoModuleReleaseReachesAFreshConsumer(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	const name = "layer-go-module-release"

	target := livetest.Accept(t, provider.ForgeForgejo)

	path, err := goModulePath(target.Host, target.Owner, livetest.ScratchRepo(name))
	if err != nil {
		t.Skipf("Go cannot fetch a module from this forge by its tag: %v", err)
	}

	livetest.RequireVisibility(t, false, "a downstream module fetches the release without credentials")

	release := runLayerRelease(t, name, false, "sha1", "", func(files map[string]string) {
		named, err := releaseWorkflowWithBinaryName(files[releaseWorkflowFile])
		if err != nil {
			t.Fatal(err)
		}

		files[releaseWorkflowFile] = named
		files["go.mod"] = "module " + path + "\n\ngo 1.27.4\n"
		files["greeting/greeting.go"] = goModuleLibrary
	})

	// A release that fell short has no version to offer a consumer.
	if t.Failed() {
		return
	}

	consumer := livetest.NewPublicScratchRepo(t, target, "go-module-consumer", "it runs a downstream module's job, which holds no credential and no code under test")

	if conclusion := livetest.RunWorkflow(t, target, consumer, "go-module-consumer",
		goModuleConsumerProbe(livetest.TrustLabCA(target), target.Host, path, releaseFinalTag, release)); conclusion != "success" {
		t.Fatalf("the downstream module's job concluded %q: Go did not resolve %s@%s from the forge at the release commit %s, or did not run the library's code",
			conclusion, path, releaseFinalTag, release)
	}

	t.Logf("module %s@%s resolved from %s at the signed release commit %s by a fresh downstream module", path, releaseFinalTag, target.Host, release)
}
