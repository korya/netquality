# Public target compatibility evidence

The JSON files in this directory exercise config parsing; a URL in a config
fixture does not establish the size of the response it serves.

On **2026-09-10**, fresh config fetches named the small URLs below. GETs with
`Accept-Encoding: identity` returned HTTP/2 200, declared the listed
`Content-Length`, and completed with the same number of body bytes:

| Target | Config | Small response URL | Declared / received bytes |
|---|---|---|---|
| Apple | [Config](https://mensura.cdn-apple.com/api/v1/gm/config) | [Small](https://mensura.cdn-apple.com/api/v1/gm/small) | 1 / 1 |
| Cloudflare | [Config](https://aim.cloudflare.com/responsiveness/api/v1/config) | [Small](https://h3.speed.cloudflare.com/__down?bytes=10) | 10 / 10 |

These are dated observations, not vendor guarantees. They support the fixed
ten-byte compatibility ceiling in LAT-11. Apple's observed one-byte body
agrees with the draft and reference server; Cloudflare's observed ten-byte
body requires the documented deviation. A future config or body change may
require revisiting compatibility.

To repeat just the response checks without starting any load flows:

```sh
curl --fail --silent --show-error --max-time 10 --max-filesize 32 \
  --header 'Accept-Encoding: identity' --output /dev/null \
  --write-out 'status=%{http_code} bytes=%{size_download}\n' \
  'https://mensura.cdn-apple.com/api/v1/gm/small'
curl --fail --silent --show-error --max-time 10 --max-filesize 32 \
  --header 'Accept-Encoding: identity' --output /dev/null \
  --write-out 'status=%{http_code} bytes=%{size_download}\n' \
  'https://h3.speed.cloudflare.com/__down?bytes=10'
```

In native Windows shells, use `curl.exe`, put each command on one line, and
use `NUL` in place of `/dev/null`. Fetch each target's current config first
if its advertised URL may have changed. The curl limits bound
response consumption and waiting, not HTTP/TLS or socket buffering.

`TestProbeResponseSize` exercises one- and ten-byte bodies offline over
HTTP/1.1 and HTTP/2. `TestLive` exercises both full public targets in the
opt-in/nightly suite. Full live measurements were not run for this check.
