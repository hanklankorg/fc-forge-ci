# Hello Go Service

A tiny Go HTTP service, a demo of the `go-service` example for the live tests.
`my-service`, from `cmd/my-service`, answers `GET /` with a greeting and
`GET /health` with `ok`, on port 8080, using the standard library only.

Its CI files are the example's, filled in as an adopter would, and are
rewritten from it rather than edited here; `just demo` puts the Forge CI
under test in place of the pinned one. The Forgejo release builds the
binaries and the runtime image from `Containerfile.forgejo`; the GitLab file
is the merge-request gate.
