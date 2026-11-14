# Hello npm Service

A tiny npm service, a demo for the live tests: a package and the
container image that runs it, in the fictional `@example-org` scope.

`src/server.js` answers `GET /` with a greeting and `GET /health` with `ok`,
on port 8080, with Node's own `http` module and no dependencies. `npm test`
runs the tests with `node --test`; `npm run build` writes `dist/`, the files
the package ships. The `Containerfile` copies that `dist/` from the packed
package into a Node runtime image.

One release publishes three outputs: the packed package and its Build SBOM
as signed files of the release, the same package in the forge's package
registry (`publish-to: [forge-packages]` in `.forge-ci/artifacts.yml`), and
the signed image. On GitHub the release orchestrator publishes the package to
GitHub Packages, which takes it only under the owner's scope: name it
`@<owner>/hello-npm-service`. On Forgejo the last job of
`.forgejo/workflows/release.yml` publishes the released tarball with
`forge-ci publish forge-packages deploy`, using `RELEASE_REGISTRY_TOKEN`, a
token that may write the owner's packages. On GitLab the release policy's
`Publish: npm packages` job publishes the released tarball to the project's
package registry with `RELEASE_PACKAGE_TOKEN`, a deploy token of the project
that may write its packages.

A package version is published once. Forgejo keeps packages under the owner,
not the repository, so a second release of the same version by the same
owner is refused: use another version.

Its workflows name Forge CI where it is published, at a placeholder commit;
`just demo` puts the Forge CI under test in its place.
