# reManager integration metadata

The proposed metadata in this directory adds package-specific actions after the
uploader is available in Vellum:

1. **Link handwritten.blog** runs the device-code flow.
2. **Send notebook** invokes reManager's searchable notebook-selection hook.
3. The selected action sends only the canonical UUID to `handwritten-blog sync`.
4. **Connection status** is read-only.
5. **Unlink handwritten.blog** asks for confirmation before revoking the token.

The metadata is ultimately operated by the reManager maintainer at
`https://remanager.io/remanager-metadata.json`. It should be published only
after a reManager release includes the handwritten.blog hooks and Vellum ships
uploader 0.2.0.
