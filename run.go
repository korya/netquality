package netquality

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/korya/netquality/internal/engine"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sync"
)

// Run executes the responsiveness test against t and returns the Result.
//
// If ctx is cancelled mid-run, Run returns the partial Result (Cancelled=true)
// together with ctx.Err(). If a load phase fails before completing an
// interval, Run returns the partial Result (that direction flagged
// reason=flow_error, everything measured before it intact) together with the
// error. Only discovery failures return a nil Result.
func Run(ctx context.Context, t Target, o Options) (*Result, error) {
	return RunWithEvents(ctx, t, o, nil)
}

// RunWithEvents is Run with a progress sink. sink may be nil; otherwise it
// must be safe for concurrent use, because flow and probe goroutines call it
// (see Event).
func RunWithEvents(ctx context.Context, t Target, o Options, sink func(Event)) (*Result, error) {
	r := &runner{opts: o.withDefaults(), sink: sink}
	return r.run(ctx, t)
}

type runner struct {
	opts         Options
	sink         func(Event)
	factory      *transportFactory
	cfg          *ServerConfig
	res          *Result
	mu           sync.Mutex // guards res.Warnings and res.Target.Proxy
	chainChecked bool       // first verified TLS handshake decides interception
}

// observeTLS inspects the first successful handshake for TLS interception.
func (r *runner) observeTLS(cs tls.ConnectionState) {
	r.mu.Lock()
	if r.chainChecked {
		r.mu.Unlock()
		return
	}
	r.chainChecked = true
	info := inspectChain(cs)
	if info == nil {
		r.mu.Unlock()
		return
	}
	if p := r.res.Target.Proxy; p != nil {
		p.TLSInterception, p.Issuer = true, info.Issuer
		p.Reason += "; " + info.Reason
	} else {
		r.res.Target.Proxy = info
	}
	r.mu.Unlock()
	r.warn("TLS interception: %s", info.Reason)
}

func (r *runner) emit(e Event) {
	if r.sink != nil {
		e.Time = r.opts.clock.Now()
		r.sink(e)
	}
}

func (r *runner) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.res.Warnings = append(r.res.Warnings, msg)
	r.mu.Unlock()
	r.opts.Logger.Warn(msg)
	r.emit(Event{Kind: EventWarning, Message: msg})
}

func (r *runner) run(ctx context.Context, t Target) (*Result, error) {
	start := r.opts.clock.Now()
	r.res = &Result{SchemaVersion: ResultSchemaVersion, StartedAt: start, Target: ResolvedTarget{ConfigURL: t.ConfigURL}}
	finish := func() {
		r.res.Duration = r.opts.clock.Now().Sub(start)
		if r.factory != nil { // also on cancelled exits: the network is what the caller wants to know
			r.res.Target.ResolvedIPs = r.factory.remote.list()
			r.res.Target.LocalIPs = r.factory.local.list()
		}
	}

	r.emit(Event{Kind: EventPhase, Phase: "discover", Message: t.ConfigURL})
	cfg, err := r.discover(ctx, t)
	if err != nil {
		return nil, err
	}
	r.cfg = cfg
	u, _ := url.Parse(cfg.SmallDownloadURL)
	r.res.Target.Host = u.Host
	r.res.Target.TestEndpoint = cfg.TestEndpoint
	r.res.Target.Config = *cfg

	var fwarn []string
	r.factory, fwarn = newTransportFactory(r.opts.HTTPClient, cfg, u)
	for _, w := range fwarn {
		r.warn("%s", w)
	}
	if pu := r.factory.explicitProxy(cfg.SmallDownloadURL); pu != nil {
		r.mu.Lock()
		r.res.Target.Proxy = &ProxyInfo{Explicit: true, URL: pu.String(),
			Reason: fmt.Sprintf("requests routed via proxy %s; latency and throughput measure the client→proxy leg", pu)}
		r.mu.Unlock()
		r.warn("explicit proxy %s: latency and throughput measure the client→proxy leg", pu)
		if r.factory.testEndpoint != "" {
			r.warn("test_endpoint %q is ignored because a proxy dials the origin", cfg.TestEndpoint)
		}
	}

	if r.opts.IdleProbes > 0 {
		r.emit(Event{Kind: EventPhase, Phase: "idle"})
		idle, err := r.idle(ctx)
		if ctx.Err() != nil {
			r.res.Cancelled = true
			finish()
			return r.res, ctx.Err()
		}
		if err != nil {
			r.warn("idle latency: %v", err)
		} else {
			r.res.Idle = idle
		}
	}

	dirs := []Directions{Download, Upload}
	switch r.opts.Directions {
	case Download:
		dirs = []Directions{Download}
	case Upload:
		dirs = []Directions{Upload}
	}
	for _, d := range dirs {
		r.emit(Event{Kind: EventPhase, Phase: d.String(), Direction: d.String()})
		dr, err := r.loadPhase(ctx, d)
		if d == Download {
			r.res.Download = dr
		} else {
			r.res.Upload = dr
		}
		if dr != nil && r.res.Target.HTTPVersion == "" {
			r.res.Target.HTTPVersion = dr.HTTPVersion
		}
		if ctx.Err() != nil {
			r.res.Cancelled = true
			finish()
			return r.res, ctx.Err()
		}
		if err != nil {
			// Keep what was measured (the other direction, idle latency) and
			// hand it back with the error; the failed direction is flagged.
			finish()
			return r.res, err
		}
	}
	r.emit(Event{Kind: EventPhase, Phase: "done"})
	finish()
	return r.res, nil
}

// discover fetches and validates the configuration document. Redirects are
// treated as failures, per the server spec.
func (r *runner) discover(ctx context.Context, t Target) (*ServerConfig, error) {
	if t.ConfigURL == "" {
		return nil, errors.New("netquality: empty config URL")
	}
	cctx, cancel := context.WithTimeout(ctx, r.opts.ConfigTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, t.ConfigURL, nil)
	if err != nil {
		return nil, fmt.Errorf("netquality: config url: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	setProbeHeaders(req, r.opts.Header)
	rt := r.opts.HTTPClient.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	// Use a throwaway clone so the config connection does not linger in the
	// caller's pool after Run returns (INV-4). A custom RoundTripper cannot be
	// cloned and is used as-is.
	if t, ok := rt.(*http.Transport); ok {
		c := t.Clone()
		c.DisableKeepAlives = true
		defer c.CloseIdleConnections()
		rt = c
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("netquality: fetch config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("netquality: fetch config: unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("netquality: read config: %w", err)
	}
	cfg, err := ParseServerConfig(body)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// idle measures idle latency with sequential fresh-connection probes.
func (r *runner) idle(ctx context.Context) (*LatencyStats, error) {
	rt := r.factory.newTransport(false)
	defer closeIdle(rt)
	var samples []LatencySample
	var lastErr error
	for i := 0; i < r.opts.IdleProbes; i++ {
		if ctx.Err() != nil {
			break
		}
		s, err := foreignProbe(ctx, rt, r.cfg.SmallDownloadURL, r.opts.Header, r.opts.clock.Now, r.observeTLS)
		if err != nil {
			lastErr = err
			continue
		}
		samples = append(samples, s)
		r.emit(Event{Kind: EventProbe, Phase: "idle", ProbeKind: "idle", Latency: s.Total})
	}
	if len(samples) == 0 {
		if lastErr == nil {
			lastErr = errors.New("no samples")
		}
		return nil, lastErr
	}
	st := engine.ComputeLatencyStats(samples)
	return &st, nil
}

// phaseState is the mutable state of one load phase.
type phaseState struct {
	dir     Directions
	url     string
	bytes   *byteCounter
	eng     *engine.Engine
	flows   []*flow
	flowsMu sync.Mutex

	samplesMu sync.Mutex
	curF      []LatencySample // samples of the interval in progress
	curS      []LatencySample

	// stop can be called from any flow or probe goroutine (a flow error, the
	// byte cap) as well as from the phase loop, so everything it writes is
	// guarded: stopOnce picks the winner, stopMu makes the outcome readable.
	stopOnce sync.Once
	stopMu   sync.Mutex
	reason   TruncationReason
	flowErr  error
	flowErrs int
	cancel   context.CancelFunc
}

func (p *phaseState) stop(reason TruncationReason) {
	p.stopOnce.Do(func() {
		p.stopMu.Lock()
		p.reason = reason
		p.stopMu.Unlock()
		p.cancel()
	})
}

func (p *phaseState) stopReason() TruncationReason {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	return p.reason
}

// flowFailed records a load-flow failure and aborts the phase (draft 5.4).
func (p *phaseState) flowFailed(err error) {
	p.stopMu.Lock()
	p.flowErrs++
	if p.flowErr == nil {
		p.flowErr = err
	}
	p.stopMu.Unlock()
	p.stop(ReasonFlowError)
}

func (p *phaseState) flowErrors() (int, error) {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	return p.flowErrs, p.flowErr
}

func (p *phaseState) addSample(self bool, s LatencySample) {
	p.samplesMu.Lock()
	defer p.samplesMu.Unlock()
	if self {
		p.curS = append(p.curS, s)
	} else {
		p.curF = append(p.curF, s)
	}
}

// take returns and clears the samples of the interval in progress.
func (p *phaseState) take() (foreign, self []LatencySample) {
	p.samplesMu.Lock()
	defer p.samplesMu.Unlock()
	foreign, self = p.curF, p.curS
	p.curF, p.curS = nil, nil
	return
}

func (p *phaseState) pickFlow(rng *rand.Rand) *flow {
	p.flowsMu.Lock()
	defer p.flowsMu.Unlock()
	var ready []*flow
	for _, f := range p.flows {
		if f.ready.Load() {
			ready = append(ready, f)
		}
	}
	if len(ready) == 0 {
		return nil
	}
	return ready[rng.Intn(len(ready))]
}

func (p *phaseState) flowCount() int {
	p.flowsMu.Lock()
	defer p.flowsMu.Unlock()
	return len(p.flows)
}

func (p *phaseState) proto() string {
	p.flowsMu.Lock()
	defer p.flowsMu.Unlock()
	for _, f := range p.flows {
		if s := f.proto.Load(); s != nil {
			return *s
		}
	}
	return ""
}

// loadPhase runs one direction: ramp flows, probe, evaluate stability, stop on
// stability or a limit.
func (r *runner) loadPhase(ctx context.Context, dir Directions) (*DirectionResult, error) {
	sp := r.opts.Stability
	if dir == Upload && sp.SendBufferBytes == 0 {
		// Upload bytes are counted when the transport takes them, ahead of
		// the wire by up to the HTTP/2 stream window per flow.
		sp.SendBufferBytes = DefaultUploadSendBuffer
	}
	pctx, cancel := context.WithTimeout(ctx, r.opts.MaxDuration)
	defer cancel()

	p := &phaseState{dir: dir, cancel: cancel}
	p.url = r.cfg.LargeDownloadURL
	if dir == Upload {
		p.url = r.cfg.UploadURL
	}
	p.bytes = &byteCounter{limit: r.opts.MaxBytes, onLimit: func() { p.stop(ReasonBytesCap) }}

	var wg sync.WaitGroup
	addFlow := func() {
		p.flowsMu.Lock()
		f := &flow{id: len(p.flows), rt: r.factory.newTransport(true)}
		p.flows = append(p.flows, f)
		n := len(p.flows)
		p.flowsMu.Unlock()
		r.emit(Event{Kind: EventFlow, Phase: dir.String(), Direction: dir.String(), Flows: n})
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := runFlow(pctx, f, dir, p.url, p.bytes, r.opts.Header, r.observeTLS)
			if err != nil && !errors.Is(err, errFlowDone) {
				r.opts.Logger.Error("flow failed", "dir", dir.String(), "flow", f.id, "err", err)
				p.flowFailed(err) // draft 5.4: abort on flow error
			}
		}()
	}
	p.eng = engine.New(sp, r.opts.MaxFlows)
	for i := 0; i < p.eng.InitialFlows(); i++ {
		addFlow()
	}

	// Probe scheduler.
	var probeWG sync.WaitGroup
	probeWG.Add(1)
	go func() {
		defer probeWG.Done()
		r.probeLoop(pctx, p)
	}()

	start := r.opts.clock.Now()
	tick := r.opts.clock.NewTicker(sp.Interval)
	defer tick.Stop()
	dr := &DirectionResult{Direction: dir.String()}
	var lastTick = start
loop:
	for {
		select {
		case <-pctx.Done():
			break loop
		case now := <-tick.C():
			if now.IsZero() {
				now = r.opts.clock.Now()
			}
			elapsed := now.Sub(lastTick)
			if elapsed <= 0 {
				elapsed = sp.Interval
			}
			lastTick = now
			f, sl := p.take()
			d := p.eng.Interval(engine.Observation{Elapsed: elapsed, Bytes: p.bytes.payloadBytes(), Flows: p.flowCount(), Foreign: f, Self: sl})
			dr.Intervals = d.Interval
			ev := Event{Kind: EventInterval, Phase: dir.String(), Direction: dir.String(),
				Interval: d.Interval, Flows: p.flowCount(), ThroughputBPS: d.ThroughputBPS, Bytes: p.bytes.get(), RPM: d.RPM}
			r.emit(ev)
			if d.Stop {
				r.opts.Logger.Info("responsiveness stable", "dir", dir.String(), "rpm", d.RPM)
				p.stop(ReasonNone)
				break loop
			}
			for i := 0; i < d.AddFlows; i++ {
				addFlow()
			}
		}
	}
	// Determine why we stopped, then tear everything down. stop() is
	// once-guarded, so a flow or probe goroutine that already named a reason
	// wins; reading p.reason here to pre-empt it would race with them.
	if pctx.Err() != nil {
		p.stop(ctxReason(pctx))
	}
	cancel()
	wg.Wait()
	probeWG.Wait()
	p.flowsMu.Lock()
	for _, f := range p.flows {
		closeIdle(f.rt)
	}
	p.flowsMu.Unlock()

	dr.Duration = r.opts.clock.Now().Sub(start)
	dr.Bytes = p.bytes.get()
	dr.Flows = p.flowCount()
	flowErrs, flowErr := p.flowErrors()
	dr.FlowErrors = flowErrs
	dr.HTTPVersion = p.proto()
	curF, curS := p.take()
	sum := p.eng.Summary(curF, curS)
	dr.ThroughputBPS = sum.ThroughputBPS
	dr.PeakThroughputBPS = sum.PeakThroughputBPS
	if dr.Duration > 0 {
		dr.MeanThroughputBPS = float64(p.bytes.payloadBytes()) * 8 / dr.Duration.Seconds()
	}
	if dr.Intervals == 0 {
		dr.ThroughputBPS = dr.MeanThroughputBPS
	}
	dr.ThroughputStable = sum.ThroughputStable
	dr.ThroughputConfidence = sum.ThroughputConfidence
	dr.ResponsivenessStable = sum.ResponsivenessStable
	dr.ResponsivenessConfidence = sum.ResponsivenessConfidence
	dr.Reason = p.stopReason()
	dr.Truncated = dr.Reason != ReasonNone
	dr.RPM, dr.ForeignRPM, dr.SelfRPM = sum.RPM, sum.ForeignRPM, sum.SelfRPM
	if len(sum.Foreign) > 0 {
		st := engine.ComputeLatencyStats(sum.Foreign)
		dr.Loaded.Foreign = &st
	}
	if len(sum.Self) > 0 {
		st := engine.ComputeLatencyStats(sum.Self)
		dr.Loaded.Self = &st
	}
	if len(sum.Foreign)+len(sum.Self) > 0 {
		combined := make([]LatencySample, 0, len(sum.Foreign)+len(sum.Self))
		for _, x := range sum.Foreign {
			combined = append(combined, LatencySample{Total: x.HTTP})
		}
		for _, x := range sum.Self {
			combined = append(combined, LatencySample{Total: x.HTTP})
		}
		st := engine.ComputeLatencyStats(combined)
		dr.Loaded.Combined = &st
	}

	switch dr.Reason {
	case ReasonBytesCap:
		r.warn("%s: byte cap MaxBytes=%d hit before stabilisation; result truncated", dir, r.opts.MaxBytes)
	case ReasonDurationCap:
		r.warn("%s: duration cap (%s) hit before stabilisation; result truncated", dir, r.opts.MaxDuration)
	case ReasonFlowError:
		r.warn("%s: a load flow failed (%v); phase aborted per draft", dir, flowErr)
	}
	if dr.HTTPVersion != "" && dr.HTTPVersion != "HTTP/2.0" {
		r.warn("%s: server negotiated %s; self probes unavailable, RPM uses foreign probes only", dir, dr.HTTPVersion)
	}
	if ctx.Err() != nil {
		return dr, ctx.Err()
	}
	if dr.Reason == ReasonFlowError && dr.Intervals == 0 {
		return dr, fmt.Errorf("netquality: %s: load flow failed: %w", dir, flowErr)
	}
	return dr, nil
}

// probeLoop launches interleaved foreign and self probes at a rate bounded by
// MPS and by PTC of the current goodput estimate.
func (r *runner) probeLoop(ctx context.Context, p *phaseState) {
	rng := rand.New(rand.NewSource(r.opts.clock.Now().UnixNano())) //nolint:gosec // flow selection only
	foreignRT := r.factory.newTransport(false)
	defer closeIdle(foreignRT)
	const maxInFlight = 64
	sem := make(chan struct{}, maxInFlight)
	var wg sync.WaitGroup
	defer wg.Wait()

	launch := func(self bool) {
		select {
		case sem <- struct{}{}:
		default:
			return // too many in flight; skip this slot
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			var s LatencySample
			var err error
			kind := "foreign"
			if self {
				kind = "self"
				f := p.pickFlow(rng)
				if f == nil {
					return
				}
				if pr := f.proto.Load(); pr == nil || *pr != "HTTP/2.0" {
					return // cannot multiplex on HTTP/1.1
				}
				p.bytes.addProbe(selfProbeBytes)
				s, err = selfProbe(ctx, f.rt, r.cfg.SmallDownloadURL, r.opts.Header, r.opts.clock.Now)
			} else {
				p.bytes.addProbe(foreignProbeBytes)
				s, err = foreignProbe(ctx, foreignRT, r.cfg.SmallDownloadURL, r.opts.Header, r.opts.clock.Now, r.observeTLS)
			}
			if err != nil {
				if ctx.Err() == nil {
					r.opts.Logger.Debug("probe failed", "kind", kind, "err", err)
				}
				return
			}
			p.addSample(self, s)
			r.emit(Event{Kind: EventProbe, Phase: p.dir.String(), Direction: p.dir.String(), ProbeKind: kind, Latency: s.Total})
		}()
	}

	self := false
	for {
		// Interval between individual probes: 1/MPS, stretched so that probe
		// traffic stays under PTC of the measured goodput.
		gap := p.eng.ProbeGap(foreignProbeBytes, selfProbeBytes)
		select {
		case <-ctx.Done():
			return
		case <-r.opts.clock.After(gap):
		}
		launch(self)
		self = !self
	}
}

// MarshalJSON keeps Directions readable in JSON output.
func (d Directions) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }
