# Proxy detection

Reporting when the measured path is client→proxy rather than client→server.
Detection never changes how the test is run or how numbers are computed.

Prefix: `PROXY`.

---

### PROXY-1: Explicit proxy
When the supplied transport's proxy function returns a proxy for the small
download URL, `target.proxy.explicit=true`, `target.proxy.url` holds the proxy
URL with credentials removed, and a warning says numbers cover the
client→proxy leg.

### PROXY-2: HTTP/2 through proxies
Load flows and probes tunnel through a CONNECT proxy and still negotiate
HTTP/2 when the origin supports it.

### PROXY-3: TLS interception
On the first successful, verified TLS handshake of a run, if the leaf
certificate has no Certificate Transparency SCTs (embedded or via the TLS
extension), `target.proxy.tls_interception=true`, `issuer` names the leaf's
issuer, and `reason` explains; a warning is recorded.

### PROXY-4: Inspection vendors
When the issuer names a known TLS-inspection product (e.g. Zscaler, Netskope),
`reason` says so explicitly; otherwise it says "TLS-inspecting proxy or a
private CA".

### PROXY-5: No false positives on skipped verification
When certificate verification is disabled (`--insecure`, `InsecureSkipVerify`),
interception is never reported.

### PROXY-6: Custom transports
With a non-`*http.Transport` `RoundTripper`, neither detection runs;
`target.proxy` is absent and a warning notes the limitation.

### PROXY-7: Confidence unaffected
Proxy findings do not alter confidence, truncation, or RPM.
