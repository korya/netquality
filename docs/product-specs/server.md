# Reference server (`nqserver`)

The self-hostable test server and the `server` package behind it.

Prefix: `SRV`.

---

### SRV-1: Endpoints
`/.well-known/nq` serves the config; `/nq/small` returns 200 with one byte;
`/nq/large` streams `--large-size` bytes (default 8 GiB) of incompressible
data; `/nq/upload` discards a POST/PUT body and replies with the byte count.

### SRV-2: Config content
The config advertises the three URLs under the draft names, with the prefix
from `--base-url` or, when unset, `https://` plus the request's Host; it
includes `test_endpoint` only when `--test-endpoint` is set.

### SRV-3: Methods
Small accepts GET and HEAD; large accepts GET; upload accepts POST and PUT.
Anything else is 405.

### SRV-4: Response hygiene
Bodies use `application/octet-stream`, `Cache-Control: no-store`, no content
encoding, and no redirects.

### SRV-5: TLS and HTTP/2
TLS is mandatory: `--self-signed` generates a certificate for localhost,
loopback, and the listen host; otherwise `--cert/--key` are required and their
absence is a usage error. ALPN offers `h2` then `http/1.1`.

### SRV-6: Lifecycle
`--listen` defaults to `:8443`; the server logs its bound address, shuts down
gracefully on interrupt, and exits 1 if it cannot listen.
