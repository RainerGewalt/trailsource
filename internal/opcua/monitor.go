package opcua

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// monitor is the one subscription of a session and the monitored items in
// it, keyed by the NodeId the server uses in this session.
type monitor struct {
	sub      *opcua.Subscription
	notifyCh chan *opcua.PublishNotificationData
	byHandle map[uint32]*boundNode
	byNode   map[string]uint32 // NodeId string -> client handle
	itemIDs  map[uint32]uint32 // client handle -> server's MonitoredItemID
	next     uint32
}

func newMonitor(ctx context.Context, client *opcua.Client, interval time.Duration) (*monitor, error) {
	ch := make(chan *opcua.PublishNotificationData, 1024)
	sub, err := client.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: interval}, ch)
	if err != nil {
		return nil, err
	}
	return &monitor{sub: sub, notifyCh: ch, byHandle: map[uint32]*boundNode{}, byNode: map[string]uint32{}, itemIDs: map[uint32]uint32{}, next: 1}, nil
}

func (m *monitor) cancel() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.sub.Cancel(ctx)
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
