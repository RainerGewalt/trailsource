// Package opcua implements the OPC UA source adapter: connect, browse,
// fingerprint and diff the address space, subscribe, and publish every
// observation as a sealed Trail Evidence Record. The adapter never writes to
// the server; writes are a policy question and come later.
package opcua

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"

	"github.com/RainerGewalt/trailsource/internal/chain"
	"github.com/RainerGewalt/trailsource/internal/config"
	"github.com/RainerGewalt/trailsource/internal/evidence"
	"github.com/RainerGewalt/trailsource/internal/inventory"
	"github.com/RainerGewalt/trailsource/internal/journal"
	"github.com/RainerGewalt/trailsource/internal/mapping"
	"github.com/RainerGewalt/trailsource/internal/mqttpub"
)

// Event types emitted by this adapter.
const (
	EventValueObserved     = "source.value.observed"
	EventInventoryChanged  = "source.inventory.changed"
	EventConnectionChanged = "source.connection.changed"
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
	nodesByHandle  map[uint32]*boundNode
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
	a := &Adapter{cfg: cfg, version: version, contextValues: map[string]any{}}

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

	if cfg.Journal.Enabled {
		j, err := journal.Open(cfg.Journal.Path)
		if err != nil {
			return fmt.Errorf("open journal: %w", err)
		}
		a.journal = j
		defer j.Close()
	}

	will, _ := evidence.CanonicalJCS(map[string]any{
		"status": "down", "reason": "will", "producer_id": cfg.Producer.ID, "source_id": cfg.Source.ID,
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
		err := a.session(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("opc session ended: %v", err)
			a.setError(err)
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

func (a *Adapter) session(ctx context.Context) error {
	client, sessionInfo, err := a.connect(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	a.mu.Lock()
	a.opcConnected = true
	a.lastError = ""
	a.mu.Unlock()
	a.emitConnection("connected", "", sessionInfo)
	defer func() {
		a.mu.Lock()
		a.opcConnected = false
		reason := a.lastError
		a.mu.Unlock()
		a.emitConnection("disconnected", reason, sessionInfo)
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Close(closeCtx)
	}()

	roots := make([]*ua.NodeID, 0, len(a.cfg.Source.BrowseRoots))
	for _, r := range a.cfg.Source.BrowseRoots {
		nid, err := ua.ParseNodeID(r)
		if err != nil {
			return fmt.Errorf("invalid browse root %q: %w", r, err)
		}
		roots = append(roots, nid)
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		nodes, err := a.browseAndReconcile(ctx, client, roots, sessionInfo)
		if err != nil {
			return err
		}
		if err := a.subscribeUntilRebrowse(ctx, client, nodes); err != nil {
			return err
		}
	}
}

type sessionInfo struct {
	Auth           string `json:"auth"`
	SecurityPolicy string `json:"security_policy"`
	SecurityMode   string `json:"security_mode"`
	ServerURI      string `json:"server_uri,omitempty"`
}

func (a *Adapter) connect(ctx context.Context) (*opcua.Client, sessionInfo, error) {
	src := a.cfg.Source
	info := sessionInfo{Auth: src.Auth, SecurityPolicy: src.SecurityPolicy, SecurityMode: src.SecurityMode}
	policyURI := ua.SecurityPolicyURIPrefix + src.SecurityPolicy
	mode := messageSecurityMode(src.SecurityMode)

	opts := []opcua.Option{
		opcua.RequestTimeout(a.cfg.RequestTimeout()),
		opcua.AutoReconnect(true),
		opcua.ReconnectInterval(2 * time.Second),
	}
	if src.CertificateFile != "" {
		opts = append(opts, opcua.CertificateFile(src.CertificateFile), opcua.PrivateKeyFile(src.PrivateKeyFile))
	}

	epCtx, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout())
	endpoints, err := opcua.GetEndpoints(epCtx, src.Endpoint)
	cancel()
	tokenType := ua.UserTokenTypeAnonymous
	if src.Auth == "username" {
		tokenType = ua.UserTokenTypeUserName
	}
	if err == nil {
		ep, selErr := opcua.SelectEndpoint(endpoints, policyURI, mode)
		if selErr == nil && ep != nil {
			opts = append(opts, opcua.SecurityFromEndpoint(ep, tokenType))
			if ep.Server != nil {
				info.ServerURI = ep.Server.ApplicationURI
			}
		} else {
			log.Printf("opc: endpoint %s/%s not offered by server, using explicit settings", src.SecurityPolicy, src.SecurityMode)
			opts = append(opts, opcua.SecurityPolicy(policyURI), opcua.SecurityMode(mode))
		}
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
	log.Printf("opc connected source_id=%s endpoint=%s auth=%s policy=%s mode=%s", a.cfg.Source.ID, src.Endpoint, src.Auth, src.SecurityPolicy, src.SecurityMode)
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
func (a *Adapter) browseAndReconcile(ctx context.Context, client *opcua.Client, roots []*ua.NodeID, session sessionInfo) ([]VariableNode, error) {
	nsCtx, cancelNS := context.WithTimeout(ctx, a.cfg.RequestTimeout())
	namespaces, err := client.NamespaceArray(nsCtx)
	cancelNS()
	if err != nil {
		log.Printf("opc: namespace array unavailable: %v", err)
		namespaces = client.Namespaces()
	}

	browseCtx, cancel := context.WithTimeout(ctx, 4*a.cfg.RequestTimeout())
	nodes, err := BrowseVariables(browseCtx, client, roots, a.cfg.Source.MaxDepth, a.cfg.Source.MaxNodes, namespaces)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("browse: %w", err)
	}
	items := make([]inventory.Item, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, n.Item)
	}
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
	}
	changeKind := "initial"
	var diff inventory.Diff
	var previousFP any
	if found {
		previousFP = previous.Fingerprint
		if previous.Fingerprint == fp {
			snap.Generation = previous.Generation
			a.setInventory(fp, snap.Generation)
			log.Printf("browse complete variables=%d inventory_hash=%s generation=%d unchanged", len(items), fp, snap.Generation)
			return nodes, nil
		}
		snap.Generation = previous.Generation + 1
		changeKind = "changed"
		diff = inventory.Compute(previous.Items, items)
	}
	a.setInventory(fp, snap.Generation)
	log.Printf("browse complete variables=%d inventory_hash=%s generation=%d change=%s added=%d removed=%d changed=%d",
		len(items), fp, snap.Generation, changeKind, len(diff.Added), len(diff.Removed), len(diff.Changed))

	payload := map[string]any{
		"change_kind":          changeKind,
		"fingerprint":          fp,
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

func (a *Adapter) subscribeUntilRebrowse(ctx context.Context, client *opcua.Client, nodes []VariableNode) error {
	notifyCh := make(chan *opcua.PublishNotificationData, 1024)
	sub, err := client.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: a.cfg.SubscriptionInterval()}, notifyCh)
	if err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	defer func() {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sub.Cancel(cancelCtx)
	}()

	byHandle := map[uint32]*boundNode{}
	var handle uint32 = 1
	batch := a.cfg.Source.SubscriptionBatchSize
	for start := 0; start < len(nodes); start += batch {
		end := start + batch
		if end > len(nodes) {
			end = len(nodes)
		}
		requests := make([]*ua.MonitoredItemCreateRequest, 0, end-start)
		handles := make([]uint32, 0, end-start)
		for _, n := range nodes[start:end] {
			h := handle
			handle++
			requests = append(requests, opcua.NewMonitoredItemCreateRequestWithDefaults(n.NodeID, ua.AttributeIDValue, h))
			handles = append(handles, h)
			byHandle[h] = &boundNode{node: n, res: a.mapping.Resolve(n.Item.BrowsePath, n.Item.BrowseName)}
		}
		res, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, requests...)
		if err != nil {
			return fmt.Errorf("monitor batch %d-%d: %w", start, end, err)
		}
		for i, r := range res.Results {
			if i < len(handles) && r.StatusCode != ua.StatusOK {
				n := byHandle[handles[i]]
				log.Printf("monitor rejected path=%s node=%s status=%s", n.node.Item.BrowsePath, n.node.Item.NodeID, r.StatusCode)
				delete(byHandle, handles[i])
			}
		}
	}
	a.mu.Lock()
	a.nodesByHandle = byHandle
	a.mu.Unlock()
	log.Printf("subscribed monitored_items=%d subscription_id=%d interval=%s", len(byHandle), sub.SubscriptionID, a.cfg.SubscriptionInterval())

	rebrowse := time.NewTimer(a.cfg.RebrowseInterval())
	defer rebrowse.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-rebrowse.C:
			return nil
		case n, open := <-notifyCh:
			if !open {
				return errors.New("opc notification channel closed")
			}
			if n == nil {
				continue
			}
			if n.Error != nil {
				return fmt.Errorf("opc notification: %w", n.Error)
			}
			if dcn, isDCN := n.Value.(*ua.DataChangeNotification); isDCN {
				readAt := time.Now()
				for _, item := range dcn.MonitoredItems {
					bn, known := byHandle[item.ClientHandle]
					if !known {
						continue
					}
					a.handleValue(bn, item.Value, readAt)
				}
			}
		}
	}
}

func (a *Adapter) handleValue(bn *boundNode, dv *ua.DataValue, readAt time.Time) {
	var raw any
	status := ua.StatusCode(0)
	var sourceTS, serverTS any
	var occurredAt string
	if dv != nil {
		status = dv.Status
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
		"entity_key":           inventory.EntityKey(a.cfg.Source.ID, item.BrowsePath, item.NodeID),
		"semantic_role":        bn.res.SemanticRole,
		"semantic_role_source": bn.res.Source,
		"data_type":            item.DataType,
		"value":                value,
		"value_type":           valueType,
		"status_code":          fmt.Sprintf("0x%08X", uint32(status)),
		"status_name":          StatusName(status),
		"quality":              Quality(status),
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

func (a *Adapter) emitConnection(state, reason string, session sessionInfo) {
	payload := map[string]any{
		"state":    state,
		"endpoint": a.cfg.Source.Endpoint,
		"session":  session,
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
	}
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
		"ts":                   evidence.FormatTime(time.Now()),
	}
	a.mu.Unlock()
	if err := a.pub.PublishHealth(payload); err != nil {
		log.Printf("publish health failed: %v", err)
	}
}
