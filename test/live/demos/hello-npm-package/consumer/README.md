# Hello npm package

A dependency-free, publishable package in the fictional `@example-org` scope.
Unlike `hello-npm-service`, this fixture tests a package intended for registry
installation, with no container. `npm test` packs it, installs the tarball into
an isolated consumer offline, then checks its exported API.

The GitHub live renderer replaces engine references with the engine under test.
Run only in the authorized trial organization. The publisher selects the forge
registry; do not publish this fixture to npmjs.org. Use a fresh trial version
for each registry publication and retain the exact package/version identity
for cleanup. Public and private registry installation must be verified by the
live trial; the offline installation test does not prove registry permissions.

GitLab and Forgejo workflow coverage and development releases remain pending.
