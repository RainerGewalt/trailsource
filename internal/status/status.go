// Package status defines what TrailSource records say about a source: the
// event types (what happened), the reason codes in payload.reason_code (why
// an outcome is what it is), and the state of a source as the name of its
// last lifecycle event plus the time it was last seen. docs/STATUS.md is the
// human-readable version of this file; the two must not drift apart.
//
// Reason codes are the names of the shared catalog of the trail-evidence
// repository (schema/reason-codes.json). A code TrailSource needs that is
// not in the catalog is added there first.
package status

// Event types. They stay coarse: one per kind of record.
const (
	EventValueObserved     = "source.value.observed"
	EventInventoryChanged  = "source.inventory.changed"
	EventConnectionChanged = "source.connection.changed"
	EventBridgeRestarted   = "source.bridge.restarted" // first record of every process start
)

// Reason codes, catalog names.
const (
	ConnectionEstablished = "source.connection_established" // first session of this process
	ConnectionRestored    = "source.connection_restored"    // session after a lost or failed connection
	ConnectionLost        = "source.connection_lost"        // an open session ended with an error
	ConnectionFailed      = "source.connection_failed"      // a connect attempt failed (once per outage)
	ConnectionClosed      = "source.connection_closed"      // the adapter closed the session on shutdown
	CertificateRejected   = "source.certificate_rejected"   // the server certificate failed the trust list or the pin

	InventoryInitial = "source.inventory_initial" // first browse, no persisted snapshot
	NodeAdded        = "source.node_added"        // nodes appeared
	NodeRemoved      = "source.node_removed"      // nodes disappeared
	NodeChanged      = "source.node_changed"      // attributes of nodes that stay changed

	QualityUncertain = "source.quality_uncertain" // value delivered with quality uncertain
	QualityBad       = "source.quality_bad"       // value delivered with quality bad

	Restart       = "common.restart"        // process start, new segment anchored at the last known chain_hash
	StateLost     = "common.state_lost"     // process start without the previous chain state although evidence exists
	WillPublished = "common.will_published" // broker published the Last Will: the adapter vanished
)

// Codes lists every reason code TrailSource writes, in documentation order.
var Codes = []string{
	ConnectionEstablished, ConnectionRestored, ConnectionLost, ConnectionFailed, ConnectionClosed, CertificateRejected,
	InventoryInitial, NodeAdded, NodeRemoved, NodeChanged,
	QualityUncertain, QualityBad,
	Restart, StateLost, WillPublished,
}

// provisional maps the names TrailSource used before it adopted the catalog
// to the catalog names, so journals written then still read correctly.
// source.inventory.mixed has no single equivalent; Canonical reports it as
// unknown and callers classify the diff instead.
var provisional = map[string]string{
	"source.connection.established": ConnectionEstablished,
	"source.connection.restored":    ConnectionRestored,
	"source.connection.lost":        ConnectionLost,
	"source.connection.failed":      ConnectionFailed,
	"source.connection.closed":      ConnectionClosed,
	"source.inventory.initial":      InventoryInitial,
	"source.inventory.added":        NodeAdded,
	"source.inventory.removed":      NodeRemoved,
	"source.inventory.modified":     NodeChanged,
	"common.chain.restart":          Restart,
	"common.mqtt.will":              WillPublished,
}

// Canonical returns the catalog name of a code, translating provisional
// names. ok is false for an empty code and for source.inventory.mixed.
func Canonical(code string) (string, bool) {
	if c, isOld := provisional[code]; isOld {
		return c, true
	}
	if code == "" || code == "source.inventory.mixed" {
		return "", false
	}
	return code, true
}

// Provisional returns the provisional-to-catalog name pairs, for docs and tests.
func Provisional() map[string]string {
	out := make(map[string]string, len(provisional))
	for k, v := range provisional {
		out[k] = v
	}
	return out
}

// Inventory classifies an address-space change. The catalog allows one code
// per record, so a diff with several kinds of change gets the most severe
// one: removed nodes before changed nodes before added nodes. The diff in the
// payload keeps all of them.
func Inventory(initial bool, added, removed, changed int) string {
	switch {
	case initial:
		return InventoryInitial
	case removed > 0:
		return NodeRemoved
	case changed > 0:
		return NodeChanged
	case added > 0:
		return NodeAdded
	default:
		return NodeChanged
	}
}

// Quality returns the reason code for a value quality, or "" for good.
func Quality(quality string) string {
	switch quality {
	case "bad":
		return QualityBad
	case "uncertain":
		return QualityUncertain
	default:
		return ""
	}
}

// IsUp reports whether a connection reason code leaves the source connected.
func IsUp(code string) bool {
	return code == ConnectionEstablished || code == ConnectionRestored
}
