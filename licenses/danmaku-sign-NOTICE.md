# Vendored signing package

`internal/danmaku/sign` is a **verbatim copy** of the Apache-2.0 package
`internal/protocol/sign` from `github.com/beilunyang/openhongguo` v0.2.0
(upstream source: https://github.com/beilunyang/openhongguo/tree/v0.2.0/internal/protocol/sign).
This snapshot was carried into this independent plugin from MyGo Player commit
`69c3307650bb0a58b1aa7877fec2b4c711d14b40`; no sibling checkout is required.

- License: Apache License 2.0 — see `LICENSE` in this directory.
- Upstream copyright: the openhongguo authors. The project already ships
  `licenses/openhongguo-Apache-2.0.txt` for the same module.
- Only change: the upstream `sign_test.go` was not copied (its golden vectors
  live in the upstream module); all other files are unmodified.

## Why a copy instead of an import

The signing code lives under the module's `internal/` tree, so Go forbids
importing it from another module, and the published SDK only exposes the
`Signer` interface with an always-unavailable default implementation
(`sdk.UnavailableSigner`). There is no public signing entry point to reuse, so
the portable pure-Go implementation is vendored here instead of being
re-implemented. Re-verify this note when bumping `github.com/beilunyang/openhongguo`.

The comment endpoint accepted requests signed by this package (verified
2026-11-10 against live data), even though the comment API's official client
adds an extra nonce-derived variant; the standard scheme is sufficient.

## What is *not* copied

No code was copied from GPL-3.0 projects. From the GPL-licensed
`hongguo-drama` danmaku module only protocol facts were read (endpoint path,
request body field names and values, 30-second pagination window, response
field names) — see the comments in `../danmaku.go`.
