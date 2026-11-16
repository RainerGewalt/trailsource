package mqttpub

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// The small interface permits deterministic broker-failure tests.
type transport interface {
	Publish(string, byte, bool, interface{}) mqtt.Token
	IsConnectionOpen() bool
	Disconnect(uint)
}

type message struct {
	topic string
	body  []byte
}

// Publisher owns one worker, a bounded record queue and the latest health
// snapshot. Enqueueing never waits for MQTT. There is no durable outbox or
// application retry; protocol-level QoS retransmission remains Paho's job.
type Publisher struct {
	client       transport
	qos          byte
	health       string
	queue        chan message
	healthReady  chan struct{}
	stop         chan struct{}
	done         chan struct{}
	timeout      time.Duration
	mu           sync.Mutex
	closed       bool
	latestHealth []byte
	closeOnce    sync.Once
	dropped      atomic.Uint64
	unconfirmed  atomic.Uint64
	published    atomic.Uint64
}

func newPublisher(client transport, qos byte, health string, capacity int, timeout time.Duration) *Publisher {
	p := &Publisher{client: client, qos: qos, health: health, queue: make(chan message, capacity), healthReady: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), timeout: timeout}
	go p.run()
	return p
}

// Publish accepts a copy for delivery without retain, or counts a dropped
// record and returns immediately when full or closed. Success means queued,
// not acknowledged. The caller's local journal is unaffected.
func (p *Publisher) Publish(topic string, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		p.dropped.Add(1)
		return errors.New("mqtt publisher closed")
	}
	if len(p.queue) == cap(p.queue) {
		p.dropped.Add(1)
		p.requestHealth()
		return errors.New("mqtt queue full; record dropped from transport")
	}
	p.queue <- message{topic: topic, body: append([]byte(nil), body...)}
	return nil
}

// PublishHealth coalesces snapshots, so a slow broker cannot block the source
// or build an unbounded health backlog. Transport counters are added at send.
func (p *Publisher) PublishHealth(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return errors.New("mqtt health must be an object")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("mqtt publisher closed")
	}
	p.latestHealth = body
	p.requestHealth()
	return nil
}

func (p *Publisher) requestHealth() {
	select {
	case p.healthReady <- struct{}{}:
	default:
	}
}

func (p *Publisher) healthPayload() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.latestHealth) == 0 {
		return nil
	}
	var v map[string]any
	// Preserve large numeric fields such as seq when adding counters.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(p.latestHealth, &raw)
	v = map[string]any{}
	for k, value := range raw {
		v[k] = value
	}
	v["mqtt_queue_depth"] = len(p.queue)
	v["mqtt_queue_capacity"] = cap(p.queue)
	v["mqtt_records_dropped"] = p.dropped.Load()
	v["mqtt_records_unconfirmed"] = p.unconfirmed.Load()
	v["mqtt_records_published"] = p.published.Load()
	body, _ := json.Marshal(v)
	return body
}

func (p *Publisher) run() {
	defer close(p.done)
	for {
		// Stop takes precedence even when producers filled the queue.
		select {
		case <-p.stop:
			return
		default:
		}
		select {
		case <-p.stop:
			return
		case <-p.healthReady:
			if body := p.healthPayload(); body != nil {
				if err := p.send(p.health, body, true, p.stop, p.timeout); err != nil {
					log.Printf("mqtt health publish failed: %v", err)
				}
			}
		case m := <-p.queue:
			if !p.client.IsConnectionOpen() {
				p.dropped.Add(1)
				continue
			}
			if err := p.send(m.topic, m.body, false, p.stop, p.timeout); err != nil {
				p.unconfirmed.Add(1)
				p.requestHealth()
				log.Printf("mqtt record delivery unconfirmed: %v", err)
			} else {
				p.published.Add(1)
			}
		}
	}
}

func (p *Publisher) send(topic string, body []byte, retain bool, stop <-chan struct{}, timeout time.Duration) error {
	if !p.client.IsConnectionOpen() {
		return errors.New("mqtt disconnected")
	}
	token := p.client.Publish(topic, p.qos, retain, body)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-token.Done():
		return token.Error()
	case <-timer.C:
		return fmt.Errorf("mqtt publish timeout topic=%s", topic)
	case <-stop:
		return errors.New("mqtt publisher stopping")
	}
}

// IsConnected reports an open connection, not Paho's reconnecting state.
func (p *Publisher) IsConnected() bool { return p.client.IsConnectionOpen() }

// Close cancels the worker's acknowledgement wait and counts unsent records.
// The last health snapshot is sent best effort; it cannot be observed while
// the broker is unavailable. Paho's Publish call itself has a 20 s write bound.
func (p *Publisher) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		close(p.stop)
		p.mu.Unlock()
		<-p.done
		for len(p.queue) > 0 {
			<-p.queue
			p.dropped.Add(1)
		}
		if body := p.healthPayload(); body != nil {
			_ = p.send(p.health, body, true, nil, 500*time.Millisecond)
		}
		p.client.Disconnect(500)
	})
}
