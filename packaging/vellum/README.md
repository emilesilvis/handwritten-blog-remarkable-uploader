# Vellum packaging

`VELBUILD.in` is the draft recipe for the official Vellum catalogue. After a
version tag is published, replace `@SOURCE_SHA512@` with the SHA-512 checksum of
GitHub's tagged source archive and copy the resulting `VELBUILD` into
`packages/handwritten-blog` in a Vellum checkout.

The production package must not be submitted while its only hardware record is
on beta firmware. First validate the uploader and optional UI subpackage on a
stable RM2 running reMarkable OS 3.27. The recipe deliberately rejects 3.28 and
later because the QMD extension targets version-specific Xochitl internals.

The recipe produces two packages:

- `handwritten-blog` contains the uploader and its inactive systemd bridge;
- `handwritten-blog-ui` contains the optional QMD menu patch and depends on the
  uploader, `qt-resource-rebuilder`, and `qt-command-executor`.

Removing `handwritten-blog-ui` restores the stock notebook menus without
unlinking the tablet. The service bridge runs uploads outside Xochitl's cgroup
so restarting Xochitl does not terminate an in-progress send.

Vellum builds and signs the final APK with its own repository key. End users do
not install the handwritten.blog private-alpha key.
