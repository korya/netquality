# Discovery

How a `Target` becomes concrete URLs. Covers the configuration document,
its validation, and the `test_endpoint` override.

Prefix: `DISC`.

---

### DISC-1: Target forms
A target is a config URL. `WellKnown(host)` yields `https://<host>/.well-known/nq`;
`Apple` and `Cloudflare` are predefined targets; any explicit URL is accepted.

### DISC-2: Config document shape
The document is a JSON object with `version` (integer 1, or the string `"1"`),
`urls` containing small download, large download, and upload URLs, and an
optional `test_endpoint`. Unknown members are ignored.

### DISC-3: Accepted URL member names
Both the draft names (`small_download_url`, `large_download_url`,
`upload_url`) and the vendor names (`small_https_download_url`,
`large_https_download_url`, `https_upload_url`) are accepted; the `https_`
variants win when both are present.

### DISC-4: Rejected documents
A document is rejected — and the run fails without sending test traffic —
when `version` is not 1, a mandatory member is missing or appears more than
once, `test_endpoint` appears more than once, any URL is not `http`/`https`,
or the three URLs do not share a host.

### DISC-5: Transport rules for the config fetch
Redirects and any non-200 status fail discovery with the status in the error;
the fetch is bounded by `ConfigTimeout` (default 10 s) and reads at most
64 KiB.

### DISC-6: `test_endpoint` override
When `test_endpoint` names a different host than the URLs, every test
connection dials that host (keeping the URLs' port) while sending the URLs'
host name for TLS and HTTP. `Result.Target.TestEndpoint` records it.

### DISC-8: Caller headers on every request
`Options.Header` is added to the config fetch, every probe, and every load
request; keys set there override the defaults (for example `User-Agent`).
This is how credentials for a protected server are supplied; a server that
rejects them fails the run at discovery, before any load traffic.

### DISC-7: `test_endpoint` under a proxy
When an explicit proxy is in use the override cannot be honoured; the proxy
dials the origin, and a warning names the ignored endpoint.

### DISC-9: `test_endpoint` with a custom TLS dialer
When the caller's transport sets `DialTLSContext` (or `DialTLS`) the override
cannot be honoured either: the caller's dialer would verify the certificate
against the rewritten address. The dialer is called with the URLs' host, its
connections are still tracked and torn down with the run (INV-4), and a
warning names the ignored endpoint.
