package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Schema is the only schema identifier this implementation seals or verifies.
const Schema = "trail.evidence/1"

// Producer kinds defined by the specification.
const (
	ProducerSourceBridge = "source-bridge"
)

// Time sources defined by the specification.
const (
	TimeSourceSystem  = "system"
	TimeSourceNTP     = "ntp"
	TimeSourceUnknown = "unknown"
)

// Actor authentication kinds defined by the specification.
const (
	ActorAuthBrokerAuthenticated = "broker-authenticated"
	ActorAuthSelfDeclared        = "self-declared"
	ActorAuthOperatorToken       = "operator-token"
	ActorAuthNone                = "none"
)

// Producer identifies the producing instance.
type Producer struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Time carries the record's time semantics. RecordedAt is the moment of sealing.
type Time struct {
	RecordedAt string `json:"recorded_at"`
	OccurredAt string `json:"occurred_at,omitempty"`
	ReadAt     string `json:"read_at,omitempty"`
	Source     string `json:"source"`
}

// Actor describes who caused the event and how that identity was established.
type Actor struct {
	Auth  string   `json:"auth"`
	ID    string   `json:"id,omitempty"`
	Roles []string `json:"roles,omitempty"`
}

// Policy references the governing document (config, address space, ...).
type Policy struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Hash    string `json:"hash"`
	Status  string `json:"status,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

// NoPolicy is the policy value to use when no policy applies.
func NoPolicy() Policy {
	return Policy{ID: "none", Version: "0", Hash: GenesisHash}
}

// Redaction lists payload fields that were masked before hashing.
type Redaction struct {
	Fields []string `json:"fields"`
	Method string   `json:"method"`
}

// Record is a Trail Evidence Record v1.
type Record struct {
	Schema        string         `json:"schema"`
	Producer      Producer       `json:"producer"`
	StreamID      string         `json:"stream_id"`
	Seq           Seq            `json:"seq"` // canonical decimal string on the wire
	PrevHash      string         `json:"prev_hash"`
	EventType     string         `json:"event_type"`
	Time          Time           `json:"time"`
	Actor         Actor          `json:"actor"`
	Policy        Policy         `json:"policy"`
	Payload       any            `json:"payload"`
	ContentHash   string         `json:"content_hash"`
	ChainHash     string         `json:"chain_hash"`
	ChainRestart  bool           `json:"chain_restart,omitempty"`
	AnchorHash    string         `json:"anchor_hash,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Context       map[string]any `json:"context,omitempty"`
	Redaction     *Redaction     `json:"redaction,omitempty"`
}

// FormatTime renders t as RFC 3339 UTC with millisecond precision.
func FormatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// FormatTimeMicro renders t as RFC 3339 UTC with microsecond precision. Used
// for source and server timestamps that may carry sub-millisecond resolution.
func FormatTimeMicro(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

// Seal computes content_hash and chain_hash of r in place. All other fields
// must already be set. It validates the structural rules of the specification.
func Seal(r *Record) error {
	if err := validateStructure(r); err != nil {
		return err
	}
	payloadRaw, err := json.Marshal(r.Payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	contentHash, err := HashJSON(payloadRaw)
	if err != nil {
		return fmt.Errorf("hash payload: %w", err)
	}
	r.ContentHash = contentHash

	core, err := coreFromRecord(r)
	if err != nil {
		return err
	}
	chainHash, err := Hash(core)
	if err != nil {
		return fmt.Errorf("hash core: %w", err)
	}
	r.ChainHash = chainHash
	return nil
}

// CoreJCS returns the canonical bytes of the record core (the chain_hash input).
func CoreJCS(r *Record) ([]byte, error) {
	core, err := coreFromRecord(r)
	if err != nil {
		return nil, err
	}
	return CanonicalJCS(core)
}

func coreFromRecord(r *Record) (map[string]any, error) {
	// Round-trip through JSON so omitempty semantics and member names match
	// exactly what a verifier sees on the wire.
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("marshal record: %w", err)
	}
	var m map[string]any
	dec := json.NewDecoder(bytesReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return coreFromMap(m), nil
}

// coreMembers are the members that feed chain_hash, in no particular order.
var coreMembers = []string{"schema", "producer", "stream_id", "seq", "prev_hash", "event_type", "time", "actor", "policy", "content_hash"}

func coreFromMap(m map[string]any) map[string]any {
	core := make(map[string]any, len(coreMembers)+2)
	for _, k := range coreMembers {
		if v, ok := m[k]; ok {
			core[k] = v
		}
	}
	if v, ok := m["chain_restart"]; ok {
		core["chain_restart"] = v
	}
	if v, ok := m["anchor_hash"]; ok {
		core["anchor_hash"] = v
	}
	return core
}

func validateStructure(r *Record) error {
	var errs []error
	if r.Schema != Schema {
		errs = append(errs, fmt.Errorf("schema must be %q, got %q", Schema, r.Schema))
	}
	if r.Producer.Kind == "" || r.Producer.ID == "" || r.Producer.Version == "" {
		errs = append(errs, errors.New("producer.kind, producer.id and producer.version are required"))
	}
	if r.StreamID == "" {
		errs = append(errs, errors.New("stream_id is required"))
	}
	if !IsHash(r.PrevHash) {
		errs = append(errs, fmt.Errorf("prev_hash is not a sha256 hash: %q", r.PrevHash))
	}
	if r.EventType == "" {
		errs = append(errs, errors.New("event_type is required"))
	}
	if r.Time.RecordedAt == "" || r.Time.Source == "" {
		errs = append(errs, errors.New("time.recorded_at and time.source are required"))
	}
	if r.Actor.Auth == "" {
		errs = append(errs, errors.New("actor.auth is required"))
	}
	if r.Policy.ID == "" || r.Policy.Version == "" || !IsHash(r.Policy.Hash) {
		errs = append(errs, errors.New("policy.id, policy.version and a sha256 policy.hash are required"))
	}
	if r.ChainRestart {
		if !IsHash(r.AnchorHash) {
			errs = append(errs, errors.New("chain_restart requires anchor_hash"))
		} else if r.AnchorHash != r.PrevHash {
			errs = append(errs, errors.New("chain_restart requires prev_hash == anchor_hash"))
		}
	} else if r.AnchorHash != "" {
		errs = append(errs, errors.New("anchor_hash is only allowed with chain_restart"))
	}
	return errors.Join(errs...)
}
