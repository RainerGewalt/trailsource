package mqttpub

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type fakeToken struct {
	done chan struct{}
	err  error
}

func (t *fakeToken) Done() <-chan struct{} { return t.done }
func (t *fakeToken) Error() error          { return t.err }
func (t *fakeToken) Wait() bool            { <-t.done; return true }
func (t *fakeToken) WaitTimeout(d time.Duration) bool {
	select {
	case <-t.done:
		return true
	case <-time.After(d):
		return false
	}
}

type sent struct {
	topic  string
	body   []byte
	retain bool
	token  *fakeToken
}
type fakeTransport struct {
	connected, hold atomic.Bool
	closed          atomic.Int32
	sent            chan sent
	err             error
}

func newFake() *fakeTransport {
	f := &fakeTransport{sent: make(chan sent, 1000)}
	f.connected.Store(true)
	return f
}
func (f *fakeTransport) IsConnectionOpen() bool { return f.connected.Load() }
func (f *fakeTransport) Disconnect(uint)        { f.closed.Add(1); f.connected.Store(false) }
func (f *fakeTransport) Publish(topic string, _ byte, retain bool, body interface{}) mqtt.Token {
	token := &fakeToken{done: make(chan struct{}), err: f.err}
	if !f.hold.Load() || retain {
		close(token.done)
	}
	f.sent <- sent{topic, append([]byte(nil), body.([]byte)...), retain, token}
	return token
}
func eventually(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
func next(t *testing.T, f *fakeTransport) sent {
	t.Helper()
	select {
	case m := <-f.sent:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("no publish attempt")
		return sent{}
	}
}

func TestFrozenBrokerCannotBlockEnqueueAndDropsAreCounted(t *testing.T) {
	f := newFake()
	f.hold.Store(true)
	p := newPublisher(f, 1, "health", 2, time.Hour)
	defer p.Close()
	if err := p.Publish("values", []byte("first")); err != nil {
		t.Fatal(err)
	}
	first := next(t, f)
	if first.retain {
		t.Fatal("record was retained")
	}
	for i := 0; i < 2; i++ {
		if err := p.Publish("values", []byte("queued")); err != nil {
			t.Fatal(err)
		}
	}
	returned := make(chan error, 1)
	go func() { returned <- p.Publish("values", []byte("overflow")) }()
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("overflow accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue waited for frozen broker")
	}
	if p.dropped.Load() != 1 || len(p.queue) != 2 {
		t.Fatal("overflow accounting/queue bound")
	}
	if err := p.PublishHealth(map[string]any{"status": "down", "seq": uint64(1) << 63}); err != nil {
		t.Fatal(err)
	}
	p.Close()
	if p.dropped.Load() != 3 || p.unconfirmed.Load() != 1 || f.closed.Load() != 1 {
		t.Fatalf("shutdown counts: dropped=%d unconfirmed=%d disconnect=%d", p.dropped.Load(), p.unconfirmed.Load(), f.closed.Load())
	}
	var health map[string]json.RawMessage
	last := next(t, f)
	if !last.retain || json.Unmarshal(last.body, &health) != nil {
		t.Fatal("final retained health missing")
	}
	if string(health["mqtt_records_dropped"]) != "3" || string(health["mqtt_records_unconfirmed"]) != "1" || string(health["seq"]) != "9223372036854775808" {
		t.Fatalf("wrong/rounded health counters: %s", last.body)
	}
	if err := p.Publish("values", nil); err == nil {
		t.Fatal("closed queue accepted record")
	}
	if err := p.PublishHealth(map[string]any{}); err == nil {
		t.Fatal("closed queue accepted health")
	}
}

func TestDisconnectedRecordsAreNotReplayedAfterReconnect(t *testing.T) {
	f := newFake()
	f.connected.Store(false)
	p := newPublisher(f, 1, "health", 8, time.Second)
	defer p.Close()
	for i := 0; i < 5; i++ {
		if err := p.Publish("old", []byte("old")); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool { return p.dropped.Load() == 5 })
	if len(f.sent) != 0 {
		t.Fatal("disconnected records handed to Paho's offline storage")
	}
	f.connected.Store(true)
	if err := p.Publish("new", []byte("new")); err != nil {
		t.Fatal(err)
	}
	m := next(t, f)
	if m.topic != "new" || m.retain {
		t.Fatal("old record replayed or data retained")
	}
	eventually(t, func() bool { return p.published.Load() == 1 })
}

func TestPublishTimeoutAndErrorAreUnconfirmedNotProvenLoss(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "timeout"}[timeout], func(t *testing.T) {
			f := newFake()
			f.hold.Store(timeout)
			if !timeout {
				f.err = errors.New("broker rejected write")
			}
			p := newPublisher(f, 1, "health", 4, 20*time.Millisecond)
			defer p.Close()
			if err := p.Publish("value", []byte("one")); err != nil {
				t.Fatal(err)
			}
			m := next(t, f)
			eventually(t, func() bool { return p.unconfirmed.Load() == 1 })
			if p.dropped.Load() != 0 || p.published.Load() != 0 {
				t.Fatal("uncertain outcome misclassified")
			}
			if timeout {
				close(m.token.done)
			} // Late ACK does not prove when/what a consumer received.
		})
	}
}

func TestEnqueueCopiesCallerBytesAndPreservesOrder(t *testing.T) {
	f := newFake()
	f.hold.Store(true)
	p := newPublisher(f, 1, "health", 4, time.Hour)
	defer p.Close()
	_ = p.Publish("first", []byte("first"))
	first := next(t, f)
	body := []byte("second")
	_ = p.Publish("second", body)
	body[0] = 'X'
	f.hold.Store(false)
	close(first.token.done)
	second := next(t, f)
	if second.topic != "second" || string(second.body) != "second" {
		t.Fatalf("queued body changed: %+v", second)
	}
}

func TestConcurrentEnqueueAndClose(t *testing.T) {
	f := newFake()
	f.hold.Store(true)
	p := newPublisher(f, 1, "health", 8, time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				_ = p.Publish("value", []byte("x"))
				_ = p.PublishHealth(map[string]any{"status": "up"})
			}
		}()
	}
	p.Close()
	wg.Wait()
	p.Close()
	if f.closed.Load() != 1 {
		t.Fatal("Close not idempotent")
	}
}
