# Hello meta

A shell installer, a script library and a hidden configuration file,
distributed through Git: run `sh install.sh`. There is nothing to compile, no
container, no version file and no generated SBOM.

Its CI files are the [no-build example](../../../../../../examples/no-build/)
filled in. They name Forge CI where it is published, at a placeholder commit;
the live tier puts the Forge CI under test in its place.

The project declares `project-type: meta`, so no forge builds or bumps
anything. What a release is differs with the layer:

- **GitHub** renders the notes with `git-cliff` and sets
  `changelog.skipversionbump: true`, so main and the signed final tag stay at
  the requested commit, with every file as it was. Nothing of the checkout is
  attached to the release, so it offers no file: with nothing to checksum
  there is nothing to sign, and the release is the signed tag and its notes.
- **Forgejo and GitLab** make a signed release commit that changes the
  changelog alone, and tag it. Their signer takes a hand-off of files and
  refuses one that names none, so the release hands over one file it did not
  build: the source archive of the release commit, `source-<tag>.tar.gz`, in
  the Forgejo workflow's `build` job and in `.gitlab/build-release.sh`.

The pull-request gate lints, and the consumer's own job runs the installer;
on GitLab that is `.gitlab/check-release.sh`, before a release too.
