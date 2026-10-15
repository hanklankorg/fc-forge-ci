// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package forgejobehavior runs the shell of the Forgejo delivery layer: the
// steps of the composite actions under .forgejo/actions and of the reusable
// workflows under .forgejo/workflows. It is a test-only package.
//
// Each test extracts a step's shell from its YAML and runs it under a minimal
// environment, so it observes what the step does: which argv the engine
// receives, what reaches its environment, and how each status is classified.
// Most tests put an offline fake in the engine's place. The engine tier puts
// the real one there: the CLI built from this tree by
// scripts/ci/forgejo-binary.sh, so a verb and the shell that calls it are
// tested together and a renamed flag fails here, not inside a release job.
// The same build holds the checksum a job downloads the binary by,
// .forgejo/bin/forge-ci.sha256, to the tree
// (TestRecordedEngineChecksumIsABuildOfThisTree).
//
// Nothing here goes through the action runner, a forge, a registry or a
// container runtime; the acceptance of the layer on a real Forgejo is the
// lab's.
package forgejobehavior
