#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: CC0-1.0

# Release-candidate checks, the release policy's `checks` input, run by Forge
# CI's check job in the policy's job-image as its non-root user. That is one
# image, the Java one here, so this checks the backend, as the merge-request
# gate's check-backend does. The frontend's tests need Node, so they gate in
# the release's node build job: build-release.sh runs them there and stops
# the release when one fails, after this job and before anything is signed.
set -euo pipefail
mvn --batch-mode --no-transfer-progress --file services/backend/pom.xml verify
