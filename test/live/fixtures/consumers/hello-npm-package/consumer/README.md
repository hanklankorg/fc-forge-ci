# Hello npm package

A publishable package in the fictional `@example-org` scope, with nothing
from a registry. Unlike `hello-npm-service`, this fixture tests a package
intended for registry installation, with no container. `npm test` packs it,
installs the tarball into an isolated consumer offline, then checks its
exported API.

The package is the root of an npm workspace with one member,
`packages/greeting`, which it bundles. Forge CI sees the workspace as its root
package: one `npm ci` at the root links the member, one `npm pack` there packs
the root with the member inside, and the release bumps the root's
`package.json` and `package-lock.json`. The member keeps the version it
declares and is released only as part of the root; the tests run its own test
and require the installed tarball to carry it.

On GitHub the live tests put the Forge CI under test in place of the pinned
one, and run only in the test organization. The release publishes to the
forge's registry, never to npmjs.org, and each publication needs a version
that registry has not seen. The offline installation test does not check
registry permissions.

On Forgejo and GitLab the pull or merge-request gate runs the same tests, and
the release ships the packed tarball and its Build SBOM as signed files of the
release, the version `package.json` declares. Neither layer publishes it to a
package registry: Forgejo's has no registry publication, and the GitLab
release does not run `publish-forge-packages`.
