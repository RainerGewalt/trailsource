package opcua

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync/atomic"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// heartbeatHandle is the client handle of the monitored item on
// Server_ServerStatus_CurrentTime. Its notifications prove that the publish
// cycle is alive; they are never recorded as values.
const heartbeatHandle = math.MaxUint32

// stallTolerance stretches the keep-alive window before a silent publish
// cycle counts as stalled.
const stallTolerance = 1.5

// notifyBuffer is the number of publish responses that wait for the
// adapter, in gopcua's channel and again in the monitor's: while the
// adapter is busy, for example waiting for the MQTT broker, responses keep
// arriving and keep their arrival time.
const notifyBuffer = 1024

// notification is a publish response with the time it reached the adapter.
type notification struct {
	*opcua.PublishNotificationData
	at time.Time
}

// monitor is the one subscription of a session and the monitored items in
// it, keyed by the NodeId the server uses in this session.
type monitor struct {
	client   *opcua.Client
	interval time.Duration
	kaCount  uint32 // requested MaxKeepAliveCount

	sub      *opcua.Subscription
	raw      chan *opcua.PublishNotificationData // filled by gopcua
	notifyCh chan notification                   // filled by forward, read by the adapter
	stop     chan struct{}                       // ends forward for this subscription
	byHandle map[uint32]*boundNode
	byNode   map[string]uint32 // NodeId string -> client handle
	itemIDs  map[uint32]uint32 // client handle -> server's MonitoredItemID
	next     uint32

	// lastArrival (Unix nanoseconds) is when the last publish response that
	// carried notifications reached the adapter, stamped by forward as it
	// arrives, not when the adapter got round to it. gopcua does not pass
	// keep-alive responses on, so the heartbeat item makes every publishing
	// interval carry data.
	lastArrival atomic.Int64
	window      time.Duration // revised MaxKeepAliveCount x revised interval x tolerance
}

func newMonitor(ctx context.Context, client *opcua.Client, interval time.Duration, kaCount uint32) (*monitor, error) {
	m := &monitor{client: client, interval: interval, kaCount: kaCount}
	if err := m.open(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// open creates the subscription and its heartbeat item; monitored items are
// added by syncMonitor.
func (m *monitor) open(ctx context.Context) error {
	ch := make(chan *opcua.PublishNotificationData, notifyBuffer)
	lifetime := uint32(10000)
	if 3*m.kaCount > lifetime {
		lifetime = 3 * m.kaCount // the lifetime must cover three keep-alive periods
	}
	sub, err := m.client.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: m.interval, MaxKeepAliveCount: m.kaCount, LifetimeCount: lifetime}, ch)
	if err != nil {
		return err
	}
	hb := opcua.NewMonitoredItemCreateRequestWithDefaults(ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime), ua.AttributeIDValue, heartbeatHandle)
	hb.RequestedParameters.SamplingInterval = float64(m.interval.Milliseconds())
	hb.RequestedParameters.QueueSize = 1
	heartbeat := true
	if res, err := sub.Monitor(ctx, ua.TimestampsToReturnNeither, hb); err != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = sub.Cancel(cctx)
		cancel()
		return fmt.Errorf("heartbeat item: %w", err)
	} else if len(res.Results) == 1 && res.Results[0].StatusCode != ua.StatusOK {
		// Without the heartbeat a quiet but healthy subscription would look
		// stalled; better no supervision than false records.
		log.Printf("heartbeat item on ServerStatus.CurrentTime rejected (%s): stall detection is off for this subscription", res.Results[0].StatusCode)
		heartbeat = false
	}
	if m.stop != nil {
		close(m.stop) // the previous subscription's forwarder
	}
	m.sub, m.raw = sub, ch
	m.notifyCh, m.stop = make(chan notification, notifyBuffer), make(chan struct{})
	go m.forward(m.raw, m.notifyCh, m.stop)
	m.byHandle, m.byNode, m.itemIDs, m.next = map[uint32]*boundNode{}, map[string]uint32{}, map[uint32]uint32{}, 1
	m.lastArrival.Store(time.Now().UnixNano())
	m.window = time.Duration(float64(time.Duration(sub.RevisedMaxKeepAliveCount)*sub.RevisedPublishingInterval) * stallTolerance)
	if m.window < 2*time.Second {
		m.window = 2 * time.Second
	}
	if !heartbeat {
		m.window = 0
	}
	log.Printf("subscription %d: publishing interval %s, max keep-alive count %d, stall after %s without a publish response (0s: off)",
		sub.SubscriptionID, sub.RevisedPublishingInterval, sub.RevisedMaxKeepAliveCount, m.window)
	return nil
}

// forward stamps every publish response with the time it reached the
// adapter and passes it on, until stop is closed. A closed raw channel is
// passed on as a closed out channel.
func (m *monitor) forward(raw <-chan *opcua.PublishNotificationData, out chan<- notification, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case n, ok := <-raw:
			if !ok {
				close(out)
				return
			}
			at := time.Now()
			if n != nil && n.Error == nil {
				m.lastArrival.Store(at.UnixNano())
			}
			select {
			case out <- notification{n, at}:
			case <-stop:
				return
			}
		}
	}
}

// lastPublish is the arrival time of the last publish response.
func (m *monitor) lastPublish() time.Time {
	return time.Unix(0, m.lastArrival.Load())
}

// stalled reports whether the server's publish cycle has been silent for
// longer than the keep-alive window. It measures the server, not the
// adapter: a response that arrived while the adapter was busy, for
// example waiting for the MQTT broker, has its arrival time, and a
// response that still waits to be handled proves the cycle is alive.
func (m *monitor) stalled(now time.Time) bool {
	if m.window <= 0 || len(m.notifyCh) > 0 || len(m.raw) > 0 {
		return false
	}
	return now.Sub(m.lastPublish()) > m.window
}

// rebuild replaces a stalled subscription; the caller re-adds the monitored
// items. The new subscription is created before the old one is deleted:
// with no subscription left, gopcua pauses its publish loop, and a new
// subscription right after a pause was seen to stay silent.
func (m *monitor) rebuild(ctx context.Context) error {
	old := m.sub
	if err := m.open(ctx); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = old.Cancel(cctx) // the server may not answer; gopcua forgets it locally first
	return nil
}

func (m *monitor) cancel() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.sub.Cancel(ctx)
	m.close()
}

// close ends the forwarder; the session is over.
func (m *monitor) close() {
	if m.stop != nil {
		close(m.stop)
		m.stop = nil
	}
}

// syncMonitor makes the monitored items match the browsed nodes: items of
// nodes that are gone are deleted, new nodes are added (the server sends
// their current value once), and the remaining items get the current path,
// entity_key and mapping, which a rename or a new mapping may have changed.
func (a *Adapter) syncMonitor(ctx context.Context, m *monitor, nodes []VariableNode) error {
	want := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		want[n.Item.NodeID] = true
	}
	var gone []uint32
	for key, h := range m.byNode {
		if want[key] {
			continue
		}
		if id, ok := m.itemIDs[h]; ok {
			gone = append(gone, id)
		}
		delete(m.byNode, key)
		delete(m.byHandle, h)
		delete(m.itemIDs, h)
	}
	batch := a.cfg.Source.SubscriptionBatchSize
	for start := 0; start < len(gone); start += batch {
		end := min(start+batch, len(gone))
		if _, err := m.sub.Unmonitor(ctx, gone[start:end]...); err != nil {
			if IsConnectionError(err) {
				return fmt.Errorf("delete monitored items: %w", err)
			}
			log.Printf("delete monitored items failed: %v", err)
		}
	}

	var added []VariableNode
	for _, n := range nodes {
		bn := &boundNode{node: n, res: a.mapping.Resolve(n.Item.BrowsePath, n.Item.BrowseName)}
		if h, ok := m.byNode[n.Item.NodeID]; ok {
			m.byHandle[h] = bn
			continue
		}
		added = append(added, n)
	}
	revised := map[float64]int{} // revised sampling interval (ms) -> monitored items
	rejected := 0
	for start := 0; start < len(added); start += batch {
		end := min(start+batch, len(added))
		requests := make([]*ua.MonitoredItemCreateRequest, 0, end-start)
		handles := make([]uint32, 0, end-start)
		for _, n := range added[start:end] {
			h := m.next
			m.next++
			req := opcua.NewMonitoredItemCreateRequestWithDefaults(n.NodeID, ua.AttributeIDValue, h)
			req.RequestedParameters.SamplingInterval = float64(a.cfg.SamplingInterval().Milliseconds())
			requests = append(requests, req)
			handles = append(handles, h)
			m.byHandle[h] = &boundNode{node: n, res: a.mapping.Resolve(n.Item.BrowsePath, n.Item.BrowseName)}
		}
		res, err := m.sub.Monitor(ctx, ua.TimestampsToReturnBoth, requests...)
		if err != nil {
			for _, h := range handles {
				delete(m.byHandle, h)
			}
			return fmt.Errorf("monitor batch %d-%d: %w", start, end, err)
		}
		for i, h := range handles {
			bn := m.byHandle[h]
			if i >= len(res.Results) || res.Results[i].StatusCode != ua.StatusOK {
				status := ua.StatusBad
				if i < len(res.Results) {
					status = res.Results[i].StatusCode
				}
				log.Printf("monitor rejected path=%s node=%s status=%s", bn.node.Item.BrowsePath, bn.node.Item.NodeID, status)
				delete(m.byHandle, h)
				rejected++
				continue
			}
			m.byNode[bn.node.Item.NodeID] = h
			m.itemIDs[h] = res.Results[i].MonitoredItemID
			revised[res.Results[i].RevisedSamplingInterval]++
		}
	}
	log.Printf("subscribed monitored_items=%d added=%d removed=%d rejected=%d subscription_id=%d interval=%s sampling_requested=%s sampling_revised_ms=%v",
		len(m.byHandle), len(added)-rejected, len(gone), rejected, m.sub.SubscriptionID, a.cfg.SubscriptionInterval(), a.cfg.SamplingInterval(), revised)
	return nil
}
