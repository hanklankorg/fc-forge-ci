# Hello Go CLI

A tiny Go command, a demo of the `go-cli` example for the live tests:
`my-cli` greets the name it is given, from `cmd/my-cli`.

Its CI files are the example's, filled in as an adopter would, and are
rewritten from it rather than edited here; `just demo` puts the Forge CI
under test in place of the pinned one. The Forgejo workflows gate pull
requests, build a snapshot of files and release; the GitLab file is the
merge-request gate. The GitLab release scripts in `.gitlab/` are the demo's
own, since the example ships none.
