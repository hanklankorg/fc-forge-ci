# Hello npm Service

A tiny npm service for showing Forge CI on Forge Lab: a package and the
container image that runs it.

`src/server.js` answers `GET /` with a greeting and `GET /health` with `ok`,
on port 8080, with Node's own `http` module and no dependencies. `npm test`
runs the tests with `node --test`; `npm run build` writes `dist/`, the files
the package ships. The `Containerfile` copies that `dist/` from the packed
package into a Node runtime image, so the release ships the package as a
file and the same code as an image.

Its workflows name Forge CI where it is published, at a placeholder commit;
`just demo` puts the Forge CI under test in its place.
