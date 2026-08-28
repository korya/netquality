# Invariants

Conditions that hold across the whole system. They are not individually
testable; they are upheld by code review, the architecture, and CI together.
Feature specs cite them by ID.

---

### INV-1: Bounded before start
Every run has a finite duration, byte, and connection budget fixed before the
first packet is sent. No code path may retry in a way that multiplies traffic.

### INV-2: No traffic besides the test
The library and CLI send nothing over the network except the config fetch,
probes, and load flows of the test itself. No telemetry, no result upload.

### INV-3: Every number says how it was obtained
A result never implies "not measured" with a zero. Absent phases are absent
(`nil`/omitted), truncated phases are flagged with a reason, and confidence
levels accompany converged values.

### INV-4: No work outlives `Run`
When `Run` returns, all goroutines, connections, and timers it created are
gone. Cancellation propagates to every flow and probe.

### INV-5: Standard library only
No third-party runtime dependencies; no CGO. `golang.org/x/...` needs a
written justification before it enters `go.mod`.

### INV-6: Wire behaviour follows the draft
Where `draft-ietf-ippm-responsiveness` and this project disagree, the draft
wins on the wire and the deviation is recorded in README "Deviations" with a
one-line reason.

### INV-7: Stable JSON contract
`Result` serialises with snake_case names. Fields may be added freely;
renaming, removing, retyping, or changing the meaning of a field bumps
`schema_version` (RES-9) and is recorded in the CHANGELOG, so readers of
stored documents can tell what they hold.

### INV-8: No GPL-derived code
Reference implementations under GPL (e.g. `goresponsiveness`) may be read for
behaviour, never copied or vendored. The project ships under Apache-2.0.

### INV-9: Supported toolchain
`go.mod` states a minimum Go version that still receives security fixes; CI
and releases pin an exact patch with `GOTOOLCHAIN=local` and run
`govulncheck`.

### INV-10: Injectable time and transport
All wall-clock and network access goes through the injectable clock and the
`http.Client`/`RoundTripper` supplied in `Options`, so unit tests need no
network.
