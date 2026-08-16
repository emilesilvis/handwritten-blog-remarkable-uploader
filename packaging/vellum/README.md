# Vellum packaging

`VELBUILD.in` is the draft recipe for the official Vellum catalogue. After a
version tag is published, replace `@SOURCE_SHA512@` with the SHA-512 checksum of
GitHub's tagged source archive and copy the resulting `VELBUILD` into
`packages/handwritten-blog` in a Vellum checkout.

The production package must not be submitted while its only hardware record is
on beta firmware. First validate a stable RM2 firmware and update the exact
`remarkable-os` constraint in the recipe.

Vellum builds and signs the final APK with its own repository key. End users do
not install the handwritten.blog private-alpha key.
