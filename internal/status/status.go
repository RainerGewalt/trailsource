// Package status defines the reason-code vocabulary of TrailSource records
// and the state model of a source: the name of its last lifecycle event plus
// the time it was last seen. docs/STATUS.md is the human-readable version of
// this file; the two must not drift apart.
package status

// Reason codes. Records carry them in payload.reason_code; the health topic
// carries the most recent one as last_event. source.* codes describe the
// machine-side source, common.* codes describe the adapter and its chain.
const (
	ConnectionEstablished = "source.connection.established" // first session of this process
	ConnectionRestored    = "source.connection.restored"    // session after a lost or failed connection
	ConnectionLost        = "source.connection.lost"        // an open session ended with an error
	ConnectionFailed      = "source.connection.failed"      // a connect attempt failed (once per outage)
	ConnectionClosed      = "source.connection.closed"      // the adapter closed the session on shutdown

	InventoryInitial  = "source.inventory.initial"  // first browse, no persisted snapshot
	InventoryAdded    = "source.inventory.added"    // nodes added, nothing removed or changed
	InventoryRemoved  = "source.inventory.removed"  // nodes removed, nothing added or changed
	InventoryModified = "source.inventory.modified" // attributes of existing nodes changed only
	InventoryMixed    = "source.inventory.mixed"    // more than one of added, removed, modified

	ValueObserved = "source.value.observed" // event type of value records; never a last_event

	ChainRestart = "common.chain.restart" // first record of a new chain segment (process start)
	MQTTWill     = "common.mqtt.will"     // broker published the Last Will: the adapter vanished
)

// Codes lists every reason code in documentation order.
var Codes = []string{
	ConnectionEstablished, ConnectionRestored, ConnectionLost, ConnectionFailed, ConnectionClosed,
	InventoryInitial, InventoryAdded, InventoryRemoved, InventoryModified, InventoryMixed,
	ChainRestart, MQTTWill,
}

// Inventory classifies an address-space change from the sizes of its diff.
func Inventory(initial bool, added, removed, changed int) string {
	if initial {
		return InventoryInitial
	}
	kinds := 0
	code := InventoryModified
	if added > 0 {
		kinds++
		code = InventoryAdded
	}
	if removed > 0 {
		kinds++
		code = InventoryRemoved
	}
	if changed > 0 {
		kinds++
		code = InventoryModified
	}
	if kinds > 1 {
		return InventoryMixed
	}
	return code
}

// IsUp reports whether a connection reason code leaves the source connected.
func IsUp(code string) bool {
	return code == ConnectionEstablished || code == ConnectionRestored
}
