# handwritten.blog uploader for reMarkable

This repository contains the open-source tablet client for
[handwritten.blog](https://handwritten.blog). It sends one explicitly selected
local reMarkable notebook to the user's blog as a private draft.

The uploader is unofficial and is not endorsed by reMarkable. It does not ask
for or store reMarkable Cloud credentials, and it does not access the
reMarkable Cloud.

## How it fits together

```text
reManager
  -> installs the package through Vellum
  -> lets the user link and select a notebook
  -> runs this uploader on the tablet
  -> handwritten.blog receives one private draft
```

The handwritten.blog service and backend are separate from this client and are
not part of this repository.

## User experience

The intended production installation is through the official Vellum catalogue
inside reManager. That package has not been accepted yet. Until it is, builds
from this repository are for development and controlled testing only.

Once the Vellum and reManager integrations are released, a user will:

1. connect a supported reMarkable to reManager;
2. install **handwritten.blog** from **Mods**;
3. click **Link handwritten.blog** and approve a short code in their browser;
4. click **Send notebook** and choose one notebook; and
5. review the resulting private draft on handwritten.blog.

No signing key or terminal command should be part of the production flow.

An optional, firmware-pinned UI package adds **Send to handwritten.blog** to
the open-notebook menus. The first tap explains that the tablet UI will briefly
restart; a second tap confirms the upload. This extension is intentionally
separate from the uploader because QMD patches target private Xochitl internals
and require stricter per-firmware testing. See [`ui`](ui).

## Security and privacy model

The uploader runs locally with root access because that is how third-party
software operates on supported reMarkable tablets. It can technically read the
tablet's local document store, but each upload snapshots only the UUID selected
by the user.

For a consistent snapshot, the uploader briefly stops Xochitl, copies only the
selected notebook's expected files, and restarts Xochitl before hashing or
uploading. Restart recovery also runs after interruption or copy failure. If
Xochitl was already stopped, the uploader leaves it stopped.

The linked-device credential is stored at
`/home/root/.config/handwritten-blog/config.json` with mode `0600`. It is scoped
to uploading drafts and inspecting upload status for one blog. It cannot read
the reMarkable Cloud, publish posts, or manage the user's handwritten.blog
account.

The temporary notebook archive is deleted after the request.

## Commands

```text
handwritten-blog link
handwritten-blog list [--json]
handwritten-blog sync [document-uuid]
handwritten-blog status [document-uuid]
handwritten-blog unlink
handwritten-blog purge
```

The UI package also uses four non-interactive bridge commands:
`ui-preflight`, `ui-send`, `ui-result`, and `ui-ack`. They validate canonical
UUIDs, transfer work to a system service that survives the Xochitl restart, and
return a small versioned status record to the restarted UI. They are not part
of the end-user command flow.

`list --json` is the versioned, non-interactive notebook-selection contract
used by reManager. Version 1 returns:

```json
{
  "version": 1,
  "notebooks": [
    {
      "document_id": "11111111-2222-3333-4444-555555555555",
      "title": "Morning pages"
    }
  ]
}
```

Titles are untrusted display text. Integrations must execute uploads using only
the validated canonical UUID.

## Compatibility

The current hardware record covers an RM2 running reMarkable OS `3.28.0.169`
with Vellum `0.3.1`. That OS was a beta release during testing and is not a
production support target. A stable RM2 firmware must pass the hardware gate
before the package is submitted to Vellum's production catalogue.

Other tablet models and firmware versions are unsupported until explicitly
tested and encoded in the Vellum package constraints.

## Development

Go 1.24 or later is required.

```sh
go test -race ./...
go vet ./...
scripts/build
```

`scripts/build` cross-compiles an RM2 ARMv7 binary to `dist/`. Set
`HANDWRITTEN_BLOG_ORIGIN` only when creating a controlled development build for
a different server origin.

The draft Vellum recipe lives in [`packaging/vellum`](packaging/vellum), and the
proposed reManager command metadata lives in
[`integrations/remanager`](integrations/remanager).

## License

The uploader is available under the [MIT License](LICENSE). The optional QMD
extension under [`ui`](ui) is GPL-3.0-only because it is built against the
community Xovi/QMD extension ecosystem.
