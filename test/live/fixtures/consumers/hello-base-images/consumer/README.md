# Hello base images

A tiny graph of base images, a demo of the `base-images` example for the
live tests: `base`, which starts from nothing, and `tools` and `debug`, two flavors
built on it as one stage.

Its CI files, its planner and its build contexts are the example's, filled
in as an adopter would, and are rewritten from it rather than edited here;
the live tier puts the Forge CI under test in place of the pinned one. The
Forgejo workflow releases: it builds, signs and promotes `base`,
authenticates it, builds `tools` and `debug` on that one digest, and
publishes a signed inventory of all three images. The GitLab release does
the same from a release policy: `.gitlab/base-stages` holds the graph the
live tier gives that policy as `base-stages`.
