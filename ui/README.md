# reMarkable notebook UI extension

This optional QMD extension adds **Send to handwritten.blog** to the open
notebook menus. It is deliberately separate from the uploader so removing the
UI package restores the stock interface without unlinking the tablet or
removing the uploader.

The extension is pinned to reMarkable OS 3.27. QMD patches target private,
version-specific Xochitl internals and must not be installed on untested
firmware. It requires Xovi's `qt-resource-rebuilder` and
`qt-command-executor` extensions.

The first tap performs a local preflight and asks the user to tap again within
eight seconds. The second tap starts `handwritten-blog-send@.service` in a
separate systemd cgroup. That separation is required because the uploader
briefly restarts Xochitl while taking a consistent snapshot. After Xochitl
returns, the QMD extension reads the per-notebook status handoff and displays
the result.

The QMD extension is licensed under GPL-3.0-only. The Go uploader and the rest
of this repository remain MIT-licensed.
