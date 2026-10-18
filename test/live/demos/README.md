<!--
SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government

SPDX-License-Identifier: CC0-1.0
-->

# Demos

Each directory here is one demo `just demo <forgejo|gitlab|github> <name>`
shows: a small consumer that uses Forge CI the way a project would, released
and then reviewed in a pull or merge request, and kept for people to look at.

```text
<name>/
  consumer/            the project, committed as it is into the demo repository
  job.Containerfile    optional: its GitLab job image, built by `just demo-image`
  build.Containerfile  optional: its GitLab release build image, when the job
                       image cannot build the release (Buildah for an image)
```

The consumer names Forge CI as a published project does; the live tier puts
the layer under test in its place, as it does for any consumer. Its Forgejo
workflows sit in `.forgejo/workflows`, its GitLab merge-request gate in
`.gitlab/merge-request.yml` with the scripts the release runs, and its GitHub
workflows in `test/live/github/workflows`. `hello-maven` is a Maven jar;
`hello-maven-service` adds the runtime image built from that jar. A demo for
another ecosystem or feature is one more directory in the same shape;
`internal/livetest/demos_internal_test.go` renders every consumer here for
all three forges, so one that the live tier could not run fails `just
test-unit`.

docs/testing.md, Showing a consumer, has the commands, the images each forge
needs, and how to delete a demo.
