#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Runs the application jar the way a user of the release does: the two jars
# side by side in an empty directory, `java -jar` on the application's. Every
# forge's gate calls it after the build, so a jar that lost its Main-Class or
# its class path fails the pull request.
set -euo pipefail

run="target/run-app"
rm -rf -- "${run}"
mkdir -p "${run}"
cp -- greeter/target/hello-greeter-*.jar app/target/hello-app-*.jar "${run}/"

greeting="$(java -jar "${run}"/hello-app-*.jar Ada ignored)"
test "${greeting}" = "Hello, Ada!"
printf 'java -jar printed: %s\n' "${greeting}"

# Without an argument it greets the world.
test "$(java -jar "${run}"/hello-app-*.jar)" = "Hello, world!"
