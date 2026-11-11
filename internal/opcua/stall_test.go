package opcua

import (
	"testing"
	"time"

	"github.com/gopcua/opcua"
)

// A publish response that arrived while the adapter was busy (for example
// blocked on an MQTT publish to a broker that does not answer) waits in the
// channel: the source's publish cycle is alive, so this is not a stall.
func TestQueuedResponseIsNotAStall(t *testing.T) {
	now := time.Now()
	m := &monitor{window: 3 * time.Second, notifyCh: make(chan notification, 1)}
	m.lastArrival.Store(now.Add(-15 * time.Second).UnixNano())
	m.notifyCh <- notification{&opcua.PublishNotificationData{}, now.Add(-15 * time.Second)}
	if m.stalled(now) {
		t.Fatal("a publish response waits in the channel, but the subscription is reported as stalled")
	}
	<-m.notifyCh
	if !m.stalled(now) {
		t.Fatal("15 s without any publish response, nothing queued: that is a stall")
	}
}

// The forwarder stamps each response when it arrives, whether or not the
// adapter reads it at once, and the stall measurement uses that time.
func TestForwarderStampsArrivalWhileTheAdapterIsBusy(t *testing.T) {
	m := &monitor{window: 3 * time.Second, raw: make(chan *opcua.PublishNotificationData, 4), notifyCh: make(chan notification, 4), stop: make(chan struct{})}
	defer close(m.stop)
	m.lastArrival.Store(time.Now().Add(-time.Hour).UnixNano())
	go m.forward(m.raw, m.notifyCh, m.stop)

	before := time.Now()
	m.raw <- &opcua.PublishNotificationData{SubscriptionID: 1}
	m.raw <- &opcua.PublishNotificationData{SubscriptionID: 2}
	deadline := time.After(5 * time.Second)
	for len(m.notifyCh) < 2 {
		select {
		case <-deadline:
			t.Fatal("forwarder did not pass the responses on")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	// The adapter reads them only now, as it would after a blocked publish.
	busyUntil := time.Now().Add(20 * time.Second)
	for i := 1; i <= 2; i++ {
		n := <-m.notifyCh
		if n.SubscriptionID != uint32(i) || n.at.Before(before) || n.at.After(time.Now()) {
			t.Fatalf("response %d: %+v, arrival %s", i, n.PublishNotificationData, n.at)
		}
	}
	if got := m.lastPublish(); got.Before(before) {
		t.Fatalf("last arrival %s is before the responses arrived (%s)", got, before)
	}
	if m.stalled(time.Now()) {
		t.Fatal("responses arrived a moment ago: no stall")
	}
	if !m.stalled(busyUntil) {
		t.Fatal("20 s after the last arrival with nothing queued: a stall")
	}
}

// An error notification proves nothing about the publish cycle and does
// not move the arrival time.
func TestForwarderIgnoresErrorsForTheArrivalTime(t *testing.T) {
	m := &monitor{raw: make(chan *opcua.PublishNotificationData, 1), notifyCh: make(chan notification, 1), stop: make(chan struct{})}
	defer close(m.stop)
	old := time.Now().Add(-time.Hour)
	m.lastArrival.Store(old.UnixNano())
	go m.forward(m.raw, m.notifyCh, m.stop)
	m.raw <- &opcua.PublishNotificationData{Error: errTest}
	n := <-m.notifyCh
	if n.Error != errTest || !m.lastPublish().Equal(time.Unix(0, old.UnixNano())) {
		t.Fatalf("error notification moved the arrival time: %s", m.lastPublish())
	}
}

type testError string

func (e testError) Error() string { return string(e) }

const errTest = testError("publish failed")
