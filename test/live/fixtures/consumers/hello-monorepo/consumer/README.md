# Hello Monorepo

A tiny monorepo, a demo of the `monorepo` example for the live tests: a
Maven service in `services/backend` and an npm service in `packages/frontend`,
each with a `Containerfile` for its runtime image. Both answer `GET /` with
a greeting and `GET /health` with `ok`, on port 8080.

Its CI files are the example's, filled in as an adopter would, and are
rewritten from it rather than edited here; `just demo` puts the Forge CI
under test in place of the pinned one. The GitLab file is the merge-request
gate: the lint gate and the check component running `mvn verify`,
`npm ci` and `npm test`; the Forgejo and GitHub pull-request workflows run
the same checks.

A release is one version of both services on every forge: the backend's jar
and the frontend's package as release files, an image of each, and the
package in the forge's registry. The Forgejo workflow builds each service in
a job of its own; on GitLab the release policy plans the jobs and runs
`.gitlab/check-release.sh` and `.gitlab/build-release.sh`.
