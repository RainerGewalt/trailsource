// Package opcua implements the OPC UA source adapter: connect, browse,
// fingerprint and diff the address space, subscribe, and publish every
// observation as a sealed Trail Evidence Record. The adapter never writes to
// the server; writes are a policy question and come later.
package opcua

import (
	"context"
	"crypto/sha1" //nolint:gosec // OPC UA thumbprints
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"

	"github.com/RainerGewalt/trailsource/internal/chain"
	"github.com/RainerGewalt/trailsource/internal/config"
	"github.com/RainerGewalt/trailsource/internal/evidence"
	"github.com/RainerGewalt/trailsource/internal/inventory"
	"github.com/RainerGewalt/trailsource/internal/journal"
	"github.com/RainerGewalt/trailsource/internal/mapping"
	"github.com/RainerGewalt/trailsource/internal/mqttpub"
	"github.com/RainerGewalt/trailsource/internal/status"
	"github.com/RainerGewalt/trailsource/internal/trust"
)

// Event types emitted by this adapter (see internal/status).
const (
	EventValueObserved       = status.EventValueObserved
	EventInventoryChanged    = status.EventInventoryChanged
	EventConnectionChanged   = status.EventConnectionChanged
	EventBridgeRestarted     = status.EventBridgeRestarted
	EventSubscriptionChanged = status.EventSubscriptionChanged
	EventDataGap             = status.EventDataGap
)

// Adapter holds the runtime of one OPC UA source.
type Adapter struct {
	cfg     *config.Config
	mapping *mapping.File
	sealer  *chain.Sealer
	journal *journal.Writer
	pub     *mqttpub.Publisher
	version string

	mu             sync.Mutex
	inventoryHash  string
	generation     uint64
	opcConnected   bool
	lastError      string
	recordsEmitted uint64
	contextValues  map[string]any // latest values of mapping rules flagged context: true
	lastEvent      string         // reason code of the latest lifecycle record (docs/STATUS.md)
	lastSeen       string         // when the source last answered: value, browse or connect

	// Owned by the Run goroutine.
	everConnected bool      // a session was established in this process
	outage        bool      // a lost or failed record was emitted and no session since
	outageSince   time.Time // when that record was emitted
	lastRejected  string    // thumbprint of the last rejected server certificate
}

type boundNode struct {
	node VariableNode
	res  mapping.Resolution
}

// Run starts the adapter and blocks until ctx is cancelled or setup fails.
func Run(ctx context.Context, cfg *config.Config, version string) error {
	if cfg.Producer.Version != "" {
		version = cfg.Producer.Version
	}
	a := &Adapter{cfg: cfg, version: version, contextValues: map[string]any{}, lastEvent: status.Restart}

	a.mapping = mapping.Empty()
	if cfg.MappingFile != "" {
		m, err := mapping.Load(cfg.MappingFile)
		if err != nil {
			return fmt.Errorf("load mapping: %w", err)
		}
		a.mapping = m
	}

	producer := evidence.Producer{Kind: evidence.ProducerSourceBridge, ID: cfg.Producer.ID, Version: version}
	store := chain.FileStore{Path: filepath.Join(cfg.StateDir, cfg.Source.ID+".chain.json")}
	sealer, err := chain.New(store, producer, cfg.Source.ID)
	if err != nil {
		return fmt.Errorf("open chain state: %w", err)
	}
	a.sealer = sealer

	// Evidence that exists without chain state means the state was lost,
	// not that this stream is new (common.state_lost instead of common.restart).
	priorEvidence := fileHasData(a.snapshotPath())
	if cfg.Journal.Enabled {
		priorEvidence = priorEvidence || fileHasData(cfg.Journal.Path)
		j, err := journal.Open(cfg.Journal.Path)
		if err != nil {
			return fmt.Errorf("open journal: %w", err)
		}
		a.journal = j
		defer j.Close()
	}

	will, _ := evidence.CanonicalJCS(map[string]any{
		"status": "down", "reason": "will", "producer_id": cfg.Producer.ID, "source_id": cfg.Source.ID,
		"last_event": status.WillPublished, "last_seen": nil,
	})
	pub, err := mqttpub.New(mqttpub.Options{
		Broker:             cfg.MQTT.Broker,
		ClientID:           cfg.MQTT.ClientID,
		Username:           cfg.MQTT.Username,
		Password:           cfg.MQTT.Password,
		QoS:                byte(cfg.MQTT.QoS),
		HealthTopic:        cfg.MQTT.TopicPrefix + "/health",
		WillPayload:        will,
		UseTLS:             cfg.MQTT.TLS.Enabled,
		CACertPath:         cfg.MQTT.TLS.CACert,
		ClientCertPath:     cfg.MQTT.TLS.ClientCert,
		ClientKeyPath:      cfg.MQTT.TLS.ClientKey,
		InsecureSkipVerify: cfg.MQTT.TLS.InsecureSkipVerify,
		ServerName:         cfg.MQTT.TLS.ServerName,
		OnConnect:          func() { a.publishHealth("up") },
	})
	if err != nil {
		return fmt.Errorf("mqtt: %w", err)
	}
	a.pub = pub
	defer func() {
		a.publishHealth("down")
		pub.Close()
	}()

	seq, chainHash, sealed := sealer.Position()
	log.Printf("trailsource opcua adapter %s source_id=%s endpoint=%s topic_prefix=%s config_version=%s mapping=%s chain_seq=%d chain_hash=%s resumed=%v",
		version, cfg.Source.ID, cfg.Source.Endpoint, cfg.MQTT.TopicPrefix, cfg.Version, a.mapping.Hash, seq, chainHash, sealed)
	if err := a.emitStart(sealed, priorEvidence); err != nil {
		return fmt.Errorf("emit start record: %w", err)
	}

	healthTicker := time.NewTicker(cfg.HealthInterval())
	defer healthTicker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-healthTicker.C:
				a.publishHealth("up")
			}
		}
	}()

	for {
		if ctx.Err() != nil {
			return nil
		}
		connected, err := a.session(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("opc session ended: %v", err)
			a.setError(err)
			var rej *trust.Rejection
			switch {
			case !connected && errors.As(err, &rej):
				// A rejected server certificate is its own record, once per
				// outage and certificate: an administrator has to act.
				if !a.outage || rej.Thumbprint != a.lastRejected {
					if !a.outage {
						a.outage, a.outageSince = true, time.Now()
					}
					a.lastRejected = rej.Thumbprint
					a.emitConnection("disconnected", status.CertificateRejected, err.Error(), a.configuredSession(), rejectionPayload(rej))
				}
			case !connected && !a.outage:
				// Record the first failed attempt of an outage (or of a start
				// against an unreachable server) once; retries stay silent.
				a.outage, a.outageSince = true, time.Now()
				a.emitConnection("disconnected", status.ConnectionFailed, err.Error(), a.configuredSession(), nil)
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("restarting opc session in %s", cfg.SessionBackoff())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.SessionBackoff()):
		}
	}
}

func (a *Adapter) setError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.lastError = ""
		return
	}
	a.lastError = err.Error()
}

// session runs one OPC UA session. connected reports whether the connect
// succeeded, so the caller can tell a failed attempt from a lost session.
func (a *Adapter) session(ctx context.Context) (connected bool, err error) {
	client, sessionInfo, err := a.connect(ctx)
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	a.mu.Lock()
	a.opcConnected = true
	a.lastError = ""
	a.mu.Unlock()
	code := status.ConnectionEstablished
	var extra map[string]any
	if a.everConnected || a.outage {
		code = status.ConnectionRestored
		if a.outage {
			extra = map[string]any{"down_ms": time.Since(a.outageSince).Milliseconds()}
		}
	}
	a.everConnected, a.outage = true, false
	a.seen(time.Now())
	a.emitConnection("connected", code, "", sessionInfo, extra)
	defer func() {
		a.mu.Lock()
		a.opcConnected = false
		a.mu.Unlock()
		code, reason := status.ConnectionLost, "session ended"
		switch {
		case ctx.Err() != nil:
			code, reason = status.ConnectionClosed, "adapter stopping"
		case err != nil:
			reason = err.Error()
		}
		if code == status.ConnectionLost {
			a.outage, a.outageSince = true, time.Now()
		}
		a.emitConnection("disconnected", code, reason, sessionInfo, nil)
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Close(closeCtx)
	}()

	nodes, err := a.browseAndReconcile(ctx, client, sessionInfo)
	if err != nil {
		return true, err
	}
	// One subscription for the whole session. A re-browse changes only the
	// monitored items that differ: creating a new subscription each time
	// left some servers' publish cycle silent for a whole interval and sent
	// every value again.
	mon, err := newMonitor(ctx, client, a.cfg.SubscriptionInterval(), a.cfg.KeepAliveCount())
	if err != nil {
		return true, fmt.Errorf("create subscription: %w", err)
	}
	dead := false
	defer func() {
		if !dead { // on a dead connection cancelling only waits for a timeout
			mon.cancel()
		}
	}()
	if err := a.syncMonitor(ctx, mon, nodes); err != nil {
		return true, err
	}
	dead, err = a.watch(ctx, client, sessionInfo, mon, nodes)
	return true, err
}

// configuredSession describes the session the adapter would open; used for
// records about failed attempts, where no server answered.
func (a *Adapter) configuredSession() sessionInfo {
	src := a.cfg.Source
	return sessionInfo{Auth: src.Auth, SecurityPolicy: src.SecurityPolicy, SecurityMode: src.SecurityMode}
}

// seen moves last_seen forward.
func (a *Adapter) seen(t time.Time) {
	a.mu.Lock()
	a.lastSeen = evidence.FormatTime(t)
	a.mu.Unlock()
}

type sessionInfo struct {
	Auth             string `json:"auth"`
	SecurityPolicy   string `json:"security_policy"`
	SecurityMode     string `json:"security_mode"`
	ServerURI        string `json:"server_uri,omitempty"`
	ServerThumbprint string `json:"server_certificate_thumbprint,omitempty"` // SHA-1, secured sessions only
}

func (a *Adapter) connect(ctx context.Context) (*opcua.Client, sessionInfo, error) {
	return dial(ctx, a.cfg)
}

// dial opens one read-only OPC UA session as configured. Endpoint security
// comes from the server's endpoint list when it offers the configured policy
// and mode; otherwise the explicit settings are used.
func dial(ctx context.Context, cfg *config.Config) (*opcua.Client, sessionInfo, error) {
	src := cfg.Source
	info := sessionInfo{Auth: src.Auth, SecurityPolicy: src.SecurityPolicy, SecurityMode: src.SecurityMode}
	policyURI := ua.SecurityPolicyURIPrefix + src.SecurityPolicy
	mode := messageSecurityMode(src.SecurityMode)

	// The stack does not reconnect on its own: every loss ends the session,
	// is recorded, and the next session browses and subscribes from scratch.
	opts := []opcua.Option{
		opcua.RequestTimeout(cfg.RequestTimeout()),
		opcua.AutoReconnect(false),
		opcua.ApplicationName("TrailSource"),
	}
	if src.CertificateFile != "" {
		// CertificateFile also takes the ApplicationURI from the certificate.
		opts = append(opts, opcua.CertificateFile(src.CertificateFile), opcua.PrivateKeyFile(src.PrivateKeyFile))
	} else {
		opts = append(opts, opcua.ApplicationURI(src.ApplicationURI))
	}

	epCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout())
	endpoints, err := opcua.GetEndpoints(epCtx, src.Endpoint)
	cancel()
	tokenType := ua.UserTokenTypeAnonymous
	if src.Auth == "username" {
		tokenType = ua.UserTokenTypeUserName
	}
	secured := mode != ua.MessageSecurityModeNone
	if err == nil {
		ep, selErr := opcua.SelectEndpoint(endpoints, policyURI, mode)
		if selErr == nil && ep != nil {
			if ep.Server != nil {
				info.ServerURI = ep.Server.ApplicationURI
			}
			if secured {
				// Only a server certificate that passes the trust list (and
				// the pin, when one is set) is used; nothing is accepted
				// silently. A rejected certificate lands in <pki>/rejected.
				leaf, err := (trust.Store{Dir: src.PKIDir}).Check(ep.ServerCertificate, info.ServerURI)
				if err != nil {
					return nil, info, err
				}
				info.ServerThumbprint = Thumbprint(leaf.Raw)
			}
			if err := checkPinned(src.ServerThumbprint, info.ServerThumbprint); err != nil {
				return nil, info, err
			}
			opts = append(opts, opcua.SecurityFromEndpoint(ep, tokenType))
		} else if secured || src.ServerThumbprint != "" {
			return nil, info, fmt.Errorf("the server does not offer %s/%s; without its endpoint description the server certificate cannot be checked, refusing to connect", src.SecurityPolicy, src.SecurityMode)
		} else {
			log.Printf("opc: endpoint %s/%s not offered by server, using explicit settings", src.SecurityPolicy, src.SecurityMode)
			opts = append(opts, opcua.SecurityPolicy(policyURI), opcua.SecurityMode(mode))
		}
	} else if secured || src.ServerThumbprint != "" {
		return nil, info, fmt.Errorf("endpoint list unavailable (%v); the server certificate cannot be checked, refusing to connect", err)
	} else {
		log.Printf("opc: GetEndpoints failed (%v), using explicit settings", err)
		opts = append(opts, opcua.SecurityPolicy(policyURI), opcua.SecurityMode(mode))
	}
	if src.Auth == "username" {
		opts = append(opts, opcua.AuthUsername(src.Username, src.Password))
	} else {
		opts = append(opts, opcua.AuthAnonymous())
	}

	client, err := opcua.NewClient(src.Endpoint, opts...)
	if err != nil {
		return nil, info, err
	}
	connectCtx, cancelConnect := context.WithTimeout(ctx, 20*time.Second)
	defer cancelConnect()
	if err := client.Connect(connectCtx); err != nil {
		return nil, info, err
	}
	log.Printf("opc connected source_id=%s endpoint=%s auth=%s policy=%s mode=%s", cfg.Source.ID, src.Endpoint, src.Auth, src.SecurityPolicy, src.SecurityMode)
	return client, info, nil
}

func messageSecurityMode(s string) ua.MessageSecurityMode {
	switch s {
	case "Sign":
		return ua.MessageSecurityModeSign
	case "SignAndEncrypt":
		return ua.MessageSecurityModeSignAndEncrypt
	default:
		return ua.MessageSecurityModeNone
	}
}

func (a *Adapter) snapshotPath() string {
	return filepath.Join(a.cfg.StateDir, a.cfg.Source.ID+".inventory.json")
}

// browseAndReconcile browses the address space, compares it with the persisted
// snapshot and emits source.inventory.changed when the fingerprint differs.
func (a *Adapter) browseAndReconcile(ctx context.Context, client *opcua.Client, session sessionInfo) ([]VariableNode, error) {
	nsCtx, cancelNS := context.WithTimeout(ctx, a.cfg.RequestTimeout())
	namespaces, err := client.NamespaceArray(nsCtx)
	cancelNS()
	if err != nil {
		log.Printf("opc: namespace array unavailable: %v", err)
		namespaces = client.Namespaces()
	}
	roots, err := resolveRoots(a.cfg.Source.BrowseRoots, namespaces)
	if err != nil {
		return nil, err
	}

	browseCtx, cancel := context.WithTimeout(ctx, a.cfg.BrowseTimeout())
	started := time.Now()
	res, err := BrowseVariables(browseCtx, client, roots, a.cfg.Source.MaxDepth, a.cfg.Source.MaxNodes, namespaces)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("browse: %w", err)
	}
	if st := client.State(); st != opcua.Connected {
		return nil, fmt.Errorf("browse: connection state %s after browse, result discarded", st)
	}
	nodes := res.Nodes
	if res.Truncated {
		log.Printf("browse: max_nodes=%d reached after %d nodes visited; the inventory covers a prefix of the address space", a.cfg.Source.MaxNodes, res.Visited)
	}
	a.seen(time.Now())
	items := inventoryItems(a.cfg.Source.ID, nodes)
	fp := inventory.Fingerprint(items)

	previous, found, err := inventory.Load(a.snapshotPath())
	if err != nil {
		return nil, err
	}
	now := time.Now()
	snap := &inventory.Snapshot{
		SourceID:    a.cfg.Source.ID,
		Endpoint:    a.cfg.Source.Endpoint,
		BrowseRoots: a.cfg.Source.BrowseRoots,
		Generation:  1,
		TakenAt:     evidence.FormatTime(now),
		Fingerprint: fp,
		Items:       items,

		FingerprintVersion: inventory.FingerprintVersion,
	}
	changeKind := "initial"
	var diff inventory.Diff
	var previousFP any
	rebaseline := found && previous.FingerprintVersion < inventory.FingerprintVersion
	if found {
		previousFP = previous.Fingerprint
		if previous.Fingerprint == fp {
			snap.Generation = previous.Generation
			a.setInventory(fp, snap.Generation)
			log.Printf("browse complete variables=%d visited=%d requests=%d truncated=%v took=%s inventory_hash=%s generation=%d unchanged",
				len(items), res.Visited, res.Requests, res.Truncated, time.Since(started).Round(time.Millisecond), fp, snap.Generation)
			return nodes, nil
		}
		snap.Generation = previous.Generation + 1
		changeKind = "changed"
		diff = inventory.Compute(previous.Items, items)
		if rebaseline {
			// The snapshot was fingerprinted under an older definition: the
			// hashes are not comparable, so this browse is a new baseline.
			// The diff still shows what changed by NodeId.
			changeKind = "rebaseline"
		}
	}
	a.setInventory(fp, snap.Generation)
	reasonCode := status.Inventory(changeKind != "changed", len(diff.Added), len(diff.Removed), len(diff.Changed))
	log.Printf("browse complete variables=%d visited=%d requests=%d truncated=%v took=%s inventory_hash=%s generation=%d change=%s reason_code=%s added=%d removed=%d changed=%d",
		len(items), res.Visited, res.Requests, res.Truncated, time.Since(started).Round(time.Millisecond), fp, snap.Generation, changeKind, reasonCode, len(diff.Added), len(diff.Removed), len(diff.Changed))

	payload := map[string]any{
		"reason_code":          reasonCode,
		"truncated":            res.Truncated,
		"browse_limits":        map[string]any{"max_depth": a.cfg.Source.MaxDepth, "max_nodes": a.cfg.Source.MaxNodes},
		"change_kind":          changeKind,
		"fingerprint":          fp,
		"fingerprint_version":  inventory.FingerprintVersion,
		"previous_fingerprint": previousFP,
		"generation":           snap.Generation,
		"node_count":           len(items),
		"endpoint":             a.cfg.Source.Endpoint,
		"browse_roots":         a.cfg.Source.BrowseRoots,
		"namespaces":           namespaces,
		"session":              session,
		"diff":                 diff,
		"nodes":                items,
	}
	rec := &evidence.Record{
		EventType: EventInventoryChanged,
		Time:      evidence.Time{ReadAt: evidence.FormatTime(now), Source: evidence.TimeSourceSystem},
		Actor:     a.actor(),
		Policy: evidence.Policy{
			ID:      "config:" + filepath.Base(a.cfg.Path),
			Version: a.cfg.Revision,
			Hash:    a.cfg.Version,
			Kind:    "config",
		},
		Payload: payload,
		Context: a.context(),
	}
	if err := a.emit(rec, a.cfg.MQTT.TopicPrefix+"/inventory"); err != nil {
		return nil, fmt.Errorf("emit inventory record: %w", err)
	}
	a.setLastEvent(reasonCode)
	if err := inventory.Save(a.snapshotPath(), snap); err != nil {
		return nil, fmt.Errorf("save inventory snapshot: %w", err)
	}
	return nodes, nil
}

func (a *Adapter) setInventory(fp string, gen uint64) {
	a.mu.Lock()
	a.inventoryHash = fp
	a.generation = gen
	a.mu.Unlock()
}

// watch handles notifications until the session ends. It re-browses every
// rebrowse_interval_s (sooner after a deleted node) in a goroutine, so
// notifications keep flowing during a long browse, and then adjusts the
// monitored items. dead reports that the connection is gone.
func (a *Adapter) watch(ctx context.Context, client *opcua.Client, info sessionInfo, mon *monitor, nodes []VariableNode) (dead bool, err error) {
	type browseResult struct {
		nodes []VariableNode
		err   error
	}
	browseCtx, cancelBrowse := context.WithCancel(ctx)
	var browsing chan browseResult // non-nil while a browse runs
	defer func() {
		cancelBrowse()
		if browsing != nil {
			<-browsing // never leave a browse running past the session
		}
	}()

	rebrowse := time.NewTimer(a.cfg.RebrowseInterval())
	defer rebrowse.Stop()
	lastBrowse := time.Now()
	rebrowseSoon := false
	// A stalled publish cycle: the time of the last publish response before
	// it, kept until data flows again and the gap can be declared.
	var stalledSince time.Time
	stalled := false
	// A dead server does not always close the notification channel; the
	// client state does change, so it is polled once per second. A
	// half-open connection (socket open, nothing arrives) changes neither,
	// so the server is also asked for its current time every
	// liveness_interval_s; no answer within request_timeout_s ends the
	// session as lost.
	stateCheck := time.NewTicker(time.Second)
	defer stateCheck.Stop()
	liveness := time.NewTicker(a.cfg.LivenessInterval())
	defer liveness.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case <-rebrowse.C:
			if browsing == nil {
				ch := make(chan browseResult, 1)
				browsing = ch
				go func() {
					nodes, err := a.browseAndReconcile(browseCtx, client, info)
					ch <- browseResult{nodes, err}
				}()
			}
		case r := <-browsing:
			browsing = nil
			if r.err != nil {
				return client.State() != opcua.Connected, r.err
			}
			if err := a.syncMonitor(ctx, mon, r.nodes); err != nil {
				return IsConnectionError(err), err
			}
			nodes = r.nodes
			lastBrowse, rebrowseSoon = time.Now(), false
			rebrowse.Reset(a.cfg.RebrowseInterval())
		case <-stateCheck.C:
			if st := client.State(); st != opcua.Connected {
				return true, fmt.Errorf("opc connection state %s", st)
			}
			if now := time.Now(); mon.stalled(now) {
				// Neither data nor keep-alive within the window, while the
				// session itself is still there: record it once per stall,
				// rebuild the subscription, and declare the gap when data
				// flows again.
				if !stalled {
					stalled, stalledSince = true, mon.lastPublish
					a.emitStall(mon, now)
				}
				oldID := mon.sub.SubscriptionID
				if err := mon.rebuild(ctx); err != nil {
					return IsConnectionError(err), fmt.Errorf("rebuild stalled subscription: %w", err)
				}
				log.Printf("subscription %d stalled (no publish response for %s), replaced by %d", oldID, now.Sub(stalledSince).Round(time.Millisecond), mon.sub.SubscriptionID)
				if err := a.syncMonitor(ctx, mon, nodes); err != nil {
					return IsConnectionError(err), err
				}
			}
		case <-liveness.C:
			if err := a.checkLiveness(ctx, client); err != nil {
				return true, err
			}
		case n, open := <-mon.notifyCh:
			if !open {
				return false, errors.New("opc notification channel closed")
			}
			if n == nil {
				continue
			}
			if n.Error != nil {
				return false, fmt.Errorf("opc notification: %w", n.Error)
			}
			now := time.Now()
			mon.lastPublish = now
			if stalled {
				stalled = false
				a.emitGap(stalledSince, now, status.SubscriptionStalled, mon.sub.SubscriptionID)
			}
			dcn, isDCN := n.Value.(*ua.DataChangeNotification)
			if !isDCN {
				continue
			}
			readAt := time.Now()
			a.seen(readAt)
			for _, item := range dcn.MonitoredItems {
				if item.ClientHandle == heartbeatHandle {
					continue // proves the publish cycle, is not a value of the source
				}
				bn, known := mon.byHandle[item.ClientHandle]
				if !known {
					continue // removed by the last re-browse
				}
				a.handleValue(bn, item.Value, readAt)
				if !rebrowseSoon && browsing == nil && item.Value != nil && NodeGone(item.Value.Status) {
					// The server deleted a node we monitor: the address
					// space changed, so browse again instead of waiting
					// for the interval, at most once per minRebrowse.
					rebrowseSoon = true
					delay := max(minRebrowse-time.Since(lastBrowse), 0)
					log.Printf("monitored node %s reports %s: address space changed on the server, re-browsing in %s",
						bn.node.Item.NodeID, StatusName(item.Value.Status), delay.Round(time.Millisecond))
					rebrowse.Reset(delay)
				}
			}
		}
	}
}

// checkLiveness reads ServerStatus.CurrentTime (i=2258). Any answer counts,
// even a bad status: the server responded. No answer in time is an error.
func (a *Adapter) checkLiveness(ctx context.Context, client *opcua.Client) error {
	rctx, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout())
	defer cancel()
	_, err := client.Read(rctx, &ua.ReadRequest{
		NodesToRead:        []*ua.ReadValueID{{NodeID: ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime), AttributeID: ua.AttributeIDValue}},
		TimestampsToReturn: ua.TimestampsToReturnNeither,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("server not answering: liveness read of ServerStatus.CurrentTime failed after %s: %w", a.cfg.RequestTimeout(), err)
	}
	a.seen(time.Now())
	return nil
}

// minRebrowse is the shortest time between two browses triggered by deleted
// nodes, so a server that churns its address space cannot keep the adapter
// browsing.
const minRebrowse = 5 * time.Second

// NodeGone reports whether a monitored item's status says the node no longer
// exists in the server's address space.
func NodeGone(code ua.StatusCode) bool {
	return code == ua.StatusBadNodeIDUnknown || code == ua.StatusBadNodeIDInvalid
}

func (a *Adapter) handleValue(bn *boundNode, dv *ua.DataValue, readAt time.Time) {
	var raw any
	sc := ua.StatusCode(0)
	var sourceTS, serverTS any
	var occurredAt string
	if dv != nil {
		sc = dv.Status
		if dv.Value != nil {
			raw = dv.Value.Value()
		}
		if !dv.SourceTimestamp.IsZero() {
			sourceTS = evidence.FormatTimeMicro(dv.SourceTimestamp)
			occurredAt = evidence.FormatTimeMicro(dv.SourceTimestamp)
		}
		if !dv.ServerTimestamp.IsZero() {
			serverTS = evidence.FormatTimeMicro(dv.ServerTimestamp)
		}
	}
	value, valueType := EncodeValue(raw)
	item := bn.node.Item

	a.mu.Lock()
	fp, gen := a.inventoryHash, a.generation
	if bn.res.Context {
		a.contextValues[bn.res.SemanticRole] = value
	}
	ctxValues := make(map[string]any, len(a.contextValues))
	for k, v := range a.contextValues {
		ctxValues[k] = v
	}
	a.mu.Unlock()

	payload := map[string]any{
		"native_node_id":       item.NodeID,
		"namespace_uri":        item.NamespaceURI,
		"browse_path":          item.BrowsePath,
		"browse_name":          item.BrowseName,
		"entity_key":           item.EntityKey,
		"semantic_role":        bn.res.SemanticRole,
		"semantic_role_source": bn.res.Source,
		"data_type":            item.DataType,
		"value":                value,
		"value_type":           valueType,
		"status_code":          fmt.Sprintf("0x%08X", uint32(sc)),
		"status_name":          StatusName(sc),
		"quality":              Quality(sc),
		"source_timestamp":     sourceTS,
		"server_timestamp":     serverTS,
		"inventory_hash":       fp,
		"inventory_generation": gen,
		"publish_reason":       "subscription",
		"access": map[string]any{
			"access_level":      item.AccessLevel,
			"user_access_level": item.UserAccessLevel,
			"writable":          item.Writable,
			"user_writable":     item.UserWritable,
		},
	}
	if bn.res.Unit != "" {
		payload["unit"] = bn.res.Unit
	}
	if bn.res.ScaleDecimals != nil {
		payload["scale_decimals"] = *bn.res.ScaleDecimals
		payload["engineering_value"] = ScaleInteger(value, *bn.res.ScaleDecimals)
	}
	if len(ctxValues) > 0 {
		payload["context_values"] = ctxValues
	}
	if code := status.Quality(Quality(sc)); code != "" {
		payload["reason_code"] = code
	}

	rec := &evidence.Record{
		EventType: EventValueObserved,
		Time: evidence.Time{
			OccurredAt: occurredAt,
			ReadAt:     evidence.FormatTime(readAt),
			Source:     evidence.TimeSourceSystem,
		},
		Actor: a.actor(),
		Policy: evidence.Policy{
			ID:      a.cfg.Source.ID + "/addressspace",
			Version: strconv.FormatUint(gen, 10),
			Hash:    fp,
			Kind:    "addressspace",
		},
		Payload: payload,
		Context: a.context(),
	}
	if err := a.emit(rec, a.cfg.MQTT.TopicPrefix+"/values"); err != nil {
		log.Printf("emit value failed path=%s err=%v", item.BrowsePath, err)
		a.setError(err)
		return
	}
	if a.cfg.MQTT.PublishNodeTopics {
		if tok := inventory.Token(item.BrowsePath); tok != "" {
			line, _ := evidence.CanonicalJCS(rec)
			if err := a.pub.Publish(a.cfg.MQTT.TopicPrefix+"/nodes/"+tok, line); err != nil {
				log.Printf("publish node topic failed path=%s err=%v", item.BrowsePath, err)
			}
		}
	}
}

func (a *Adapter) emitConnection(state, reasonCode, reason string, session sessionInfo, extra map[string]any) {
	payload := map[string]any{
		"state":       state,
		"reason_code": reasonCode,
		"endpoint":    a.cfg.Source.Endpoint,
		"session":     session,
	}
	for k, v := range extra {
		payload[k] = v
	}
	if reason != "" {
		payload["reason"] = reason
	}
	rec := &evidence.Record{
		EventType: EventConnectionChanged,
		Time:      evidence.Time{ReadAt: evidence.FormatTime(time.Now()), Source: evidence.TimeSourceSystem},
		Actor:     a.actor(),
		Policy: evidence.Policy{
			ID:      "config:" + filepath.Base(a.cfg.Path),
			Version: a.cfg.Revision,
			Hash:    a.cfg.Version,
			Kind:    "config",
		},
		Payload: payload,
		Context: a.context(),
	}
	if err := a.emit(rec, a.cfg.MQTT.TopicPrefix+"/status"); err != nil {
		log.Printf("emit connection record failed: %v", err)
		return
	}
	a.setLastEvent(reasonCode)
	a.publishHealth("up")
}

// emitStall records that the publish cycle went silent.
func (a *Adapter) emitStall(mon *monitor, now time.Time) {
	payload := map[string]any{
		"state":                          "stalled",
		"reason_code":                    status.SubscriptionStalled,
		"subscription_id":                mon.sub.SubscriptionID,
		"last_publish_at":                evidence.FormatTime(mon.lastPublish),
		"detected_at":                    evidence.FormatTime(now),
		"keepalive_window_ms":            mon.window.Milliseconds(),
		"revised_publishing_interval_ms": mon.sub.RevisedPublishingInterval.Milliseconds(),
		"revised_max_keepalive_count":    mon.sub.RevisedMaxKeepAliveCount,
	}
	a.emitLifecycle(EventSubscriptionChanged, payload, status.SubscriptionStalled)
}

// emitGap declares an interval in which values were not observed.
func (a *Adapter) emitGap(from, to time.Time, cause string, subscriptionID uint32) {
	payload := map[string]any{
		"reason_code":     status.Gap,
		"from":            evidence.FormatTime(from),
		"to":              evidence.FormatTime(to),
		"duration_ms":     to.Sub(from).Milliseconds(),
		"cause":           cause,
		"subscription_id": subscriptionID,
	}
	a.emitLifecycle(EventDataGap, payload, status.Gap)
}

func (a *Adapter) emitLifecycle(eventType string, payload map[string]any, code string) {
	rec := &evidence.Record{
		EventType: eventType,
		Time:      evidence.Time{ReadAt: evidence.FormatTime(time.Now()), Source: evidence.TimeSourceSystem},
		Actor:     a.actor(),
		Policy: evidence.Policy{
			ID:      "config:" + filepath.Base(a.cfg.Path),
			Version: a.cfg.Revision,
			Hash:    a.cfg.Version,
			Kind:    "config",
		},
		Payload: payload,
		Context: a.context(),
	}
	if err := a.emit(rec, a.cfg.MQTT.TopicPrefix+"/status"); err != nil {
		log.Printf("emit %s record failed: %v", eventType, err)
		return
	}
	a.setLastEvent(code)
	a.publishHealth("up")
}

// emitStart writes the first record of every process start. It carries
// chain_restart and anchor_hash (set by the sealer) and the reason code the
// catalog assigns to that record: common.restart, or common.state_lost when
// the chain state is missing although a journal or snapshot already exists.
func (a *Adapter) emitStart(resumed, priorEvidence bool) error {
	code, chainState, detail := status.Restart, "resumed", ""
	switch {
	case resumed:
	case priorEvidence:
		code, chainState = status.StateLost, "lost"
		detail = "no chain state, but the journal or inventory snapshot already holds records; the new segment is anchored at genesis"
	default:
		chainState = "new"
	}
	payload := map[string]any{
		"reason_code":      code,
		"chain_state":      chainState,
		"producer_version": a.version,
		"config_version":   a.cfg.Version,
		"mapping_hash":     a.mapping.Hash,
		"endpoint":         a.cfg.Source.Endpoint,
	}
	if detail != "" {
		payload["detail"] = detail
	}
	rec := &evidence.Record{
		EventType: EventBridgeRestarted,
		Time:      evidence.Time{ReadAt: evidence.FormatTime(time.Now()), Source: evidence.TimeSourceSystem},
		Actor:     a.actor(),
		Policy: evidence.Policy{
			ID:      "config:" + filepath.Base(a.cfg.Path),
			Version: a.cfg.Revision,
			Hash:    a.cfg.Version,
			Kind:    "config",
		},
		Payload: payload,
		Context: a.context(),
	}
	if err := a.emit(rec, a.cfg.MQTT.TopicPrefix+"/status"); err != nil {
		return err
	}
	a.setLastEvent(code)
	return nil
}

func fileHasData(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}

func (a *Adapter) setLastEvent(code string) {
	a.mu.Lock()
	a.lastEvent = code
	a.mu.Unlock()
}

// emit seals, journals and publishes a record, in that order. A record that
// could not be journaled is not published.
func (a *Adapter) emit(rec *evidence.Record, topic string) error {
	if err := a.sealer.Seal(rec); err != nil {
		return err
	}
	var line []byte
	var err error
	if a.journal != nil {
		line, err = a.journal.Append(rec)
		if err != nil {
			return fmt.Errorf("journal: %w", err)
		}
	} else {
		line, err = evidence.CanonicalJCS(rec)
		if err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.recordsEmitted++
	a.mu.Unlock()
	return a.pub.Publish(topic, line)
}

func (a *Adapter) actor() evidence.Actor {
	return evidence.Actor{Auth: evidence.ActorAuthSelfDeclared, ID: a.cfg.Producer.ID, Roles: []string{"collector"}}
}

func (a *Adapter) context() map[string]any {
	host, _ := os.Hostname()
	return map[string]any{
		"site":           a.cfg.Site,
		"line":           a.cfg.Line,
		"device":         a.cfg.Device,
		"protocol":       "opcua",
		"endpoint":       a.cfg.Source.Endpoint,
		"config_version": a.cfg.Version,
		"mapping":        map[string]any{"path": filepath.Base(a.mapping.Path), "version": a.mapping.Version, "hash": a.mapping.Hash},
		"host":           host,
	}
}

func (a *Adapter) publishHealth(status string) {
	if a.pub == nil {
		return
	}
	seq, chainHash, _ := a.sealer.Position()
	a.mu.Lock()
	payload := map[string]any{
		"status":               status,
		"producer_id":          a.cfg.Producer.ID,
		"producer_version":     a.version,
		"source_id":            a.cfg.Source.ID,
		"stream_id":            a.cfg.Source.ID,
		"protocol":             "opcua",
		"endpoint":             a.cfg.Source.Endpoint,
		"opc_connected":        a.opcConnected,
		"inventory_hash":       a.inventoryHash,
		"inventory_generation": a.generation,
		"seq":                  seq,
		"chain_hash":           chainHash,
		"config_version":       a.cfg.Version,
		"mapping_hash":         a.mapping.Hash,
		"records_emitted":      a.recordsEmitted,
		"last_error":           a.lastError,
		"last_event":           a.lastEvent,
		"last_seen":            nullIfEmpty(a.lastSeen),
		"ts":                   evidence.FormatTime(time.Now()),
	}
	a.mu.Unlock()
	if err := a.pub.PublishHealth(payload); err != nil {
		log.Printf("publish health failed: %v", err)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Thumbprint returns the SHA-1 thumbprint of a certificate as lowercase hex,
// the form OPC UA servers and tools display. When der is a chain (leaf
// first, as servers may send it), the thumbprint is the leaf's.
func Thumbprint(der []byte) string {
	if certs, err := x509.ParseCertificates(der); err == nil && len(certs) > 0 {
		der = certs[0].Raw
	}
	sum := sha1.Sum(der) //nolint:gosec // OPC UA thumbprints are SHA-1 by specification
	return hex.EncodeToString(sum[:])
}

// checkPinned compares a configured thumbprint with the one the server
// presented. Colons, spaces and case are ignored. An empty pin accepts any
// certificate: gopcua does not validate server certificates against a
// trust list, so the pin is the only check.
func checkPinned(pinned, got string) error {
	if pinned == "" {
		return nil
	}
	norm := func(s string) string {
		return strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(s))
	}
	if got == "" {
		return errors.New("server_thumbprint is set but the session is not secured (security_mode None), refusing to connect")
	}
	if norm(pinned) != norm(got) {
		return &trust.Rejection{Check: trust.CheckPinMismatch, Thumbprint: got,
			Detail: fmt.Sprintf("thumbprint does not match server_thumbprint %s", norm(pinned))}
	}
	return nil
}

// resolveRoots turns the configured browse roots into NodeIds; "nsu=" roots
// are resolved against the server's namespace array of this session.
func resolveRoots(cfgRoots []string, namespaces []string) ([]*ua.NodeID, error) {
	roots := make([]*ua.NodeID, 0, len(cfgRoots))
	for _, r := range cfgRoots {
		nid, err := ResolveNodeID(r, namespaces)
		if err != nil {
			return nil, fmt.Errorf("invalid browse root %q: %w", r, err)
		}
		roots = append(roots, nid)
	}
	return roots, nil
}

// inventoryItems collects the items of a browse and assigns their entity
// keys; the keys are written back into the nodes, which the subscription
// uses for value records.
func inventoryItems(sourceID string, nodes []VariableNode) []inventory.Item {
	items := make([]inventory.Item, len(nodes))
	for i, n := range nodes {
		items[i] = n.Item
	}
	inventory.AssignEntityKeys(sourceID, items)
	for i := range nodes {
		nodes[i].Item.EntityKey = items[i].EntityKey
	}
	return items
}

// rejectionPayload is what a certificate_rejected record says about the
// certificate, so the reviewer can find it in <pki>/rejected.
func rejectionPayload(r *trust.Rejection) map[string]any {
	out := map[string]any{"check": r.Check, "certificate_thumbprint": r.Thumbprint}
	if r.Subject != "" {
		out["certificate_subject"] = r.Subject
	}
	if !r.NotAfter.IsZero() {
		out["certificate_not_before"] = evidence.FormatTime(r.NotBefore)
		out["certificate_not_after"] = evidence.FormatTime(r.NotAfter)
	}
	if r.RejectedFile != "" {
		out["rejected_file"] = r.RejectedFile
	}
	return out
}
