package netquality

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// Run executes the responsiveness test against t and returns the Result.
//
// If ctx is cancelled mid-run, Run returns the partial Result (Cancelled=true)
// together with ctx.Err(). Other errors (discovery failure, no usable flows)
// return a nil Result.
func Run(ctx context.Context, t Target, o Options) (*Result, error) {
	return RunWithEvents(ctx, t, o, nil)
}

// RunWithEvents is Run with a progress sink. sink may be nil.
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
	r.res = &Result{StartedAt: start, Target: ResolvedTarget{ConfigURL: t.ConfigURL}}
	finish := func() { r.res.Duration = r.opts.clock.Now().Sub(start) }

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
			finish()
			return nil, err
		}
	}
	r.res.Target.ResolvedIPs = r.factory.ips()
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
	req.Header.Set("User-Agent", userAgent)
	rt := r.opts.HTTPClient.Transport
	if rt == nil {
		rt = http.DefaultTransport
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
		s, err := foreignProbe(ctx, rt, r.cfg.SmallDownloadURL, r.opts.clock.Now, r.observeTLS)
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
	st := computeLatencyStats(samples)
	return &st, nil
}

// phaseState is the mutable state of one load phase.
type phaseState struct {
	dir     Directions
	url     string
	bytes   *byteCounter
	goodput atomic.Uint64 // float64 bits of the current moving-average goodput (bps)
	flows   []*flow
	flowsMu sync.Mutex

	samplesMu sync.Mutex
	foreign   [][]LatencySample // per interval
	self      [][]LatencySample
	curF      []LatencySample
	curS      []LatencySample

	stopOnce sync.Once
	reason   TruncationReason
	cancel   context.CancelFunc
	flowErr  error
	flowErrs int
}

func (p *phaseState) stop(reason TruncationReason) {
	p.stopOnce.Do(func() {
		p.reason = reason
		p.cancel()
	})
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

// rotate closes the current interval's sample buckets.
func (p *phaseState) rotate() {
	p.samplesMu.Lock()
	defer p.samplesMu.Unlock()
	p.foreign = append(p.foreign, p.curF)
	p.self = append(p.self, p.curS)
	p.curF, p.curS = nil, nil
}

// window returns samples from the last n completed intervals.
func (p *phaseState) window(n int) (foreign, self []LatencySample) {
	p.samplesMu.Lock()
	defer p.samplesMu.Unlock()
	start := len(p.foreign) - n
	if start < 0 {
		start = 0
	}
	for i := start; i < len(p.foreign); i++ {
		foreign = append(foreign, p.foreign[i]...)
		self = append(self, p.self[i]...)
	}
	return
}

func (p *phaseState) all() (foreign, self []LatencySample) {
	return p.window(len(p.foreign) + 1)
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

// responsiveness computes the draft's RPM figures over a sample set.
func responsiveness(foreign, self []LatencySample, tmp float64) (total, foreignRPM, selfRPM float64) {
	if len(foreign) > 0 {
		tcp := trimmedMean(durationsOf(foreign, func(s LatencySample) time.Duration { return s.Connect }), tmp)
		tlsd := trimmedMean(durationsOf(foreign, func(s LatencySample) time.Duration { return s.tlsPerRTT() }), tmp)
		httpf := trimmedMean(durationsOf(foreign, func(s LatencySample) time.Duration { return s.HTTP }), tmp)
		var rtt time.Duration
		if tlsd > 0 {
			rtt = (tcp + tlsd + httpf) / 3
		} else {
			rtt = (tcp + httpf) / 2 // TCP-only case, draft 5.3.1.2
		}
		foreignRPM = rpm(rtt)
	}
	if len(self) > 0 {
		selfRPM = rpm(trimmedMean(durationsOf(self, func(s LatencySample) time.Duration { return s.HTTP }), tmp))
	}
	switch {
	case foreignRPM > 0 && selfRPM > 0:
		total = (foreignRPM + selfRPM) / 2
	case foreignRPM > 0:
		total = foreignRPM
	default:
		total = selfRPM
	}
	return
}

// loadPhase runs one direction: ramp flows, probe, evaluate stability, stop on
// stability or a limit.
func (r *runner) loadPhase(ctx context.Context, dir Directions) (*DirectionResult, error) {
	sp := r.opts.Stability
	pctx, cancel := context.WithTimeout(ctx, r.opts.MaxDuration)
	defer cancel()

	p := &phaseState{dir: dir, cancel: cancel}
	p.url = r.cfg.LargeDownloadURL
	if dir == Upload {
		p.url = r.cfg.UploadURL
	}
	p.bytes = &byteCounter{limit: r.opts.MaxBytes, onLimit: func() { p.stop(ReasonBytesCap) }}

	var wg sync.WaitGroup
	var errMu sync.Mutex
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
			err := runFlow(pctx, f, dir, p.url, p.bytes, r.observeTLS)
			if err != nil && !errors.Is(err, errFlowDone) {
				errMu.Lock()
				p.flowErrs++
				if p.flowErr == nil {
					p.flowErr = err
				}
				errMu.Unlock()
				r.opts.Logger.Error("flow failed", "dir", dir.String(), "flow", f.id, "err", err)
				p.stop(ReasonFlowError) // draft 5.4: abort on flow error
			}
		}()
	}
	for i := 0; i < sp.InitialFlows && i < r.opts.MaxFlows; i++ {
		addFlow()
	}

	// Probe scheduler.
	tp := newStabilityTracker(sp.MovingAverageDistance, sp.StdDevTolerance)
	rp := newStabilityTracker(sp.MovingAverageDistance, sp.StdDevTolerance)
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
	var lastBytes int64
	var lastTick = start
	goodputStable := false
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
			cur := p.bytes.get()
			goodput := float64(cur-lastBytes) * 8 / elapsed.Seconds()
			lastBytes = cur
			if goodput > dr.PeakThroughputBPS {
				dr.PeakThroughputBPS = goodput
			}
			avg := tp.push(goodput)
			p.goodput.Store(math.Float64bits(avg))
			dr.Intervals++
			p.rotate()

			ev := Event{Kind: EventInterval, Phase: dir.String(), Direction: dir.String(),
				Interval: dr.Intervals, Flows: p.flowCount(), ThroughputBPS: avg, Bytes: cur}
			if !goodputStable && tp.stable() {
				goodputStable = true
				r.opts.Logger.Info("throughput stable", "dir", dir.String(), "bps", avg, "interval", dr.Intervals)
			}
			if goodputStable {
				f, s := p.window(sp.MovingAverageDistance)
				cur, _, _ := responsiveness(f, s, sp.TrimmedMeanPercent)
				ev.RPM = cur
				if cur > 0 {
					rp.push(cur)
				}
				if rp.stable() {
					r.emit(ev)
					r.opts.Logger.Info("responsiveness stable", "dir", dir.String(), "rpm", cur)
					p.stop(ReasonNone)
					break loop
				}
			}
			r.emit(ev)
			if p.flowCount() < r.opts.MaxFlows {
				for i := 0; i < sp.FlowIncrement && p.flowCount() < r.opts.MaxFlows; i++ {
					addFlow()
				}
			}
		}
	}
	// Determine why we stopped, then tear everything down.
	if p.reason == ReasonNone && pctx.Err() != nil && (!goodputStable || !rp.stable()) {
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
	dr.FlowErrors = p.flowErrs
	dr.HTTPVersion = p.proto()
	dr.ThroughputBPS = tp.current()
	if dr.Duration > 0 {
		dr.MeanThroughputBPS = float64(dr.Bytes) * 8 / dr.Duration.Seconds()
	}
	if dr.Intervals == 0 {
		dr.ThroughputBPS = dr.MeanThroughputBPS
	}
	dr.ThroughputStable = tp.stable()
	dr.ThroughputConfidence = tp.confidence()
	dr.ResponsivenessStable = rp.stable()
	dr.ResponsivenessConfidence = rp.confidence()
	if !goodputStable {
		dr.ResponsivenessConfidence = ConfidenceLow
	}
	dr.Reason = p.reason
	dr.Truncated = p.reason != ReasonNone

	// Final latency figures: the draft uses the last MAD intervals; if the phase
	// was cut short before MAD intervals completed, use everything we have.
	f, s := p.window(sp.MovingAverageDistance)
	if len(f)+len(s) == 0 {
		f, s = p.all()
	}
	dr.RPM, dr.ForeignRPM, dr.SelfRPM = responsiveness(f, s, sp.TrimmedMeanPercent)
	if len(f) > 0 {
		st := computeLatencyStats(f)
		dr.Loaded.Foreign = &st
	}
	if len(s) > 0 {
		st := computeLatencyStats(s)
		dr.Loaded.Self = &st
	}
	if len(f)+len(s) > 0 {
		combined := make([]LatencySample, 0, len(f)+len(s))
		for _, x := range f {
			combined = append(combined, LatencySample{Total: x.HTTP})
		}
		for _, x := range s {
			combined = append(combined, LatencySample{Total: x.HTTP})
		}
		st := computeLatencyStats(combined)
		dr.Loaded.Combined = &st
	}

	switch dr.Reason {
	case ReasonBytesCap:
		r.warn("%s: byte cap (%d bytes) hit before stabilisation; result truncated", dir, r.opts.MaxBytes)
	case ReasonDurationCap:
		r.warn("%s: duration cap (%s) hit before stabilisation; result truncated", dir, r.opts.MaxDuration)
	case ReasonFlowError:
		r.warn("%s: a load flow failed (%v); phase aborted per draft", dir, p.flowErr)
	}
	if dr.HTTPVersion != "" && dr.HTTPVersion != "HTTP/2.0" {
		r.warn("%s: server negotiated %s; self probes unavailable, RPM uses foreign probes only", dir, dr.HTTPVersion)
	}
	if ctx.Err() != nil {
		return dr, ctx.Err()
	}
	if dr.Reason == ReasonFlowError && dr.Intervals == 0 {
		return dr, fmt.Errorf("netquality: %s: load flow failed: %w", dir, p.flowErr)
	}
	return dr, nil
}

// probeLoop launches interleaved foreign and self probes at a rate bounded by
// MPS and by PTC of the current goodput estimate.
func (r *runner) probeLoop(ctx context.Context, p *phaseState) {
	sp := r.opts.Stability
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
				p.bytes.add(selfProbeBytes)
				s, err = selfProbe(ctx, f.rt, r.cfg.SmallDownloadURL, r.opts.clock.Now)
			} else {
				p.bytes.add(foreignProbeBytes)
				s, err = foreignProbe(ctx, foreignRT, r.cfg.SmallDownloadURL, r.opts.clock.Now, r.observeTLS)
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
		gap := time.Second / time.Duration(sp.MaxProbesPerSecond)
		if bps := math.Float64frombits(p.goodput.Load()); bps > 0 {
			perProbe := float64(foreignProbeBytes+selfProbeBytes) / 2 * 8
			if g := time.Duration(perProbe / (sp.ProbeCapacityPercent * bps) * float64(time.Second)); g > gap {
				gap = g
			}
		}
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
