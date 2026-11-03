package opcua

import (
	"encoding/base64"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"time"

	"github.com/gopcua/opcua/ua"
)

// EncodeValue converts a value read from OPC UA into JSON that canonicalises
// identically in every RFC 8785 implementation. JCS treats every JSON number
// as an IEEE-754 double, so:
//
//   - Boolean, SByte..UInt32 and Float/Double stay native JSON values. A Float
//     is widened through its shortest round-trip decimal (strconv, 32-bit),
//     so 0.1f encodes as 0.1 and not 0.10000000149011612.
//   - Int64 and UInt64 are encoded as decimal strings because values beyond
//     2^53 would be rounded by canonicalisation.
//   - DateTime becomes an RFC 3339 string, ByteString base64, Guid, NodeId,
//     ExpandedNodeId and XmlElement their string forms.
//   - Variant is unwrapped (value_type "variant<inner>"), DataValue becomes
//     {value, value_type, status_code, source_timestamp, server_timestamp},
//     ExtensionObject becomes {type_id, body} with a decoded body walked
//     field by field under the same rules, or {type_id, body_base64} when the
//     stack could not decode it.
//
// The second result names the native OPC UA type so a consumer can restore
// the original representation.
func EncodeValue(v any) (any, string) {
	return encodeValue(v, 0)
}

// maxNesting bounds the walk into nested structures and arrays.
const maxNesting = 8

func encodeValue(v any, depth int) (any, string) {
	if depth > maxNesting {
		return fmt.Sprintf("%v", v), "truncated"
	}
	switch t := v.(type) {
	case nil:
		return nil, "null"
	case bool:
		return t, "bool"
	case int8:
		return int64(t), "int8"
	case int16:
		return int64(t), "int16"
	case int32:
		return int64(t), "int32"
	case int64:
		return strconv.FormatInt(t, 10), "int64"
	case int:
		return strconv.FormatInt(int64(t), 10), "int64"
	case uint8:
		return uint64(t), "uint8"
	case uint16:
		return uint64(t), "uint16"
	case uint32:
		return uint64(t), "uint32"
	case uint64:
		return strconv.FormatUint(t, 10), "uint64"
	case uint:
		return strconv.FormatUint(uint64(t), 10), "uint64"
	case float32:
		return widenFloat32(t), "float32"
	case float64:
		return finiteFloat(t), "float64"
	case string:
		return t, "string"
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano), "datetime"
	case []byte:
		return base64.StdEncoding.EncodeToString(t), "bytestring"
	case ua.XMLElement:
		return string(t), "xmlelement"
	case *ua.GUID:
		if t == nil {
			return nil, "guid"
		}
		return t.String(), "guid"
	case *ua.NodeID:
		if t == nil {
			return nil, "nodeid"
		}
		return t.String(), "nodeid"
	case *ua.ExpandedNodeID:
		if t == nil || t.NodeID == nil {
			return nil, "expandednodeid"
		}
		s := t.NodeID.String()
		if t.NamespaceURI != "" {
			s = "nsu=" + t.NamespaceURI + ";" + s
		}
		if t.ServerIndex != 0 {
			s = "svr=" + strconv.FormatUint(uint64(t.ServerIndex), 10) + ";" + s
		}
		return s, "expandednodeid"
	case *ua.LocalizedText:
		if t == nil {
			return nil, "localizedtext"
		}
		return t.Text, "localizedtext"
	case *ua.QualifiedName:
		if t == nil {
			return nil, "qualifiedname"
		}
		if t.NamespaceIndex != 0 {
			return strconv.FormatUint(uint64(t.NamespaceIndex), 10) + ":" + t.Name, "qualifiedname"
		}
		return t.Name, "qualifiedname"
	case ua.StatusCode:
		return fmt.Sprintf("0x%08X", uint32(t)), "statuscode"
	case *ua.Variant:
		if t == nil {
			return nil, "variant<null>"
		}
		inner, typ := encodeValue(t.Value(), depth+1)
		return inner, "variant<" + typ + ">"
	case *ua.DataValue:
		if t == nil {
			return nil, "datavalue"
		}
		out := map[string]any{"status_code": fmt.Sprintf("0x%08X", uint32(t.Status)), "source_timestamp": nil, "server_timestamp": nil}
		var inner any
		typ := "null"
		if t.Value != nil {
			inner, typ = encodeValue(t.Value.Value(), depth+1)
		}
		out["value"], out["value_type"] = inner, typ
		if !t.SourceTimestamp.IsZero() {
			out["source_timestamp"] = t.SourceTimestamp.UTC().Format(time.RFC3339Nano)
		}
		if !t.ServerTimestamp.IsZero() {
			out["server_timestamp"] = t.ServerTimestamp.UTC().Format(time.RFC3339Nano)
		}
		return out, "datavalue"
	case *ua.ExtensionObject:
		if t == nil {
			return nil, "extensionobject"
		}
		out := map[string]any{"type_id": nil}
		if t.TypeID != nil && t.TypeID.NodeID != nil {
			out["type_id"] = t.TypeID.NodeID.String()
		}
		switch body := t.Value.(type) {
		case nil:
			out["body"] = nil
		case []byte:
			out["body_base64"] = base64.StdEncoding.EncodeToString(body)
		default:
			out["body"], _ = encodeValue(body, depth+1)
		}
		return out, "extensionobject"
	}
	return encodeReflect(reflect.ValueOf(v), depth)
}

// encodeReflect handles arrays, matrices, decoded structures and named
// scalar types (enumerations) that the type switch does not list.
func encodeReflect(rv reflect.Value, depth int) (any, string) {
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		elemType := typeName(rv.Type().Elem())
		for i := 0; i < rv.Len(); i++ {
			out[i], elemType = encodeValue(rv.Index(i).Interface(), depth+1)
		}
		return out, "array<" + elemType + ">"
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return nil, "null"
		}
		return encodeValue(rv.Elem().Interface(), depth+1)
	case reflect.Struct:
		out := map[string]any{}
		rt := rv.Type()
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			out[f.Name], _ = encodeValue(rv.Field(i).Interface(), depth+1)
		}
		return out, "struct:" + rt.String()
	case reflect.Bool:
		return rv.Bool(), "bool:" + rv.Type().String()
	case reflect.Int8, reflect.Int16, reflect.Int32:
		return rv.Int(), "enum:" + rv.Type().String()
	case reflect.Int, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10), rv.Type().String()
	case reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return rv.Uint(), "enum:" + rv.Type().String()
	case reflect.Uint, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10), rv.Type().String()
	case reflect.Float32:
		return widenFloat32(float32(rv.Float())), rv.Type().String()
	case reflect.Float64:
		return finiteFloat(rv.Float()), rv.Type().String()
	case reflect.String:
		return rv.String(), "string:" + rv.Type().String()
	}
	return fmt.Sprintf("%v", rv.Interface()), "unknown:" + rv.Type().String()
}

func typeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Uint8:
		return "uint8"
	case reflect.Ptr:
		return typeName(t.Elem())
	}
	return t.String()
}

// finiteFloat returns f, or nil for NaN and infinities which JSON cannot carry.
func finiteFloat(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return f
}

// widenFloat32 converts a float32 to the float64 whose decimal rendering is
// the shortest representation that round-trips in 32 bits.
func widenFloat32(f float32) any {
	if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
		return nil
	}
	s := strconv.FormatFloat(float64(f), 'g', -1, 32)
	d, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return float64(f)
	}
	return d
}

// ScaleInteger applies a decimal-point scaling to an integer raw value and
// returns the engineering value as a decimal string (e.g. 2135 with 1 decimal
// -> "213.5"). It accepts the encoded forms of EncodeValue: int64/uint64 and
// their decimal string encodings. Anything else yields nil.
func ScaleInteger(v any, decimals int) any {
	var n *big.Int
	switch t := v.(type) {
	case int64:
		n = big.NewInt(t)
	case uint64:
		n = new(big.Int).SetUint64(t)
	case string:
		parsed, ok := new(big.Int).SetString(t, 10)
		if !ok {
			return nil
		}
		n = parsed
	default:
		return nil
	}
	if decimals <= 0 {
		return n.String()
	}
	neg := n.Sign() < 0
	s := new(big.Int).Abs(n).String()
	for len(s) <= decimals {
		s = "0" + s
	}
	out := s[:len(s)-decimals] + "." + s[len(s)-decimals:]
	if neg {
		out = "-" + out
	}
	return out
}

// Quality classifies an OPC UA StatusCode into good / uncertain / bad.
func Quality(code ua.StatusCode) string {
	switch {
	case uint32(code)&0x80000000 != 0:
		return "bad"
	case uint32(code)&0x40000000 != 0:
		return "uncertain"
	default:
		return "good"
	}
}

// StatusName returns the symbolic name of a StatusCode when known.
func StatusName(code ua.StatusCode) string {
	if d, found := ua.StatusCodes[code]; found {
		return d.Name
	}
	return ""
}
