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
// identically in every implementation: integers and booleans stay native,
// floating point numbers become shortest round-trip decimal strings, times
// become RFC 3339 strings. The second result names the native type.
func EncodeValue(v any) (any, string) {
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
		return t, "int64"
	case int:
		return int64(t), "int64"
	case uint8:
		return uint64(t), "uint8"
	case uint16:
		return uint64(t), "uint16"
	case uint32:
		return uint64(t), "uint32"
	case uint64:
		return t, "uint64"
	case uint:
		return uint64(t), "uint64"
	case float32:
		return formatFloat(float64(t), 32), "float32"
	case float64:
		return formatFloat(t, 64), "float64"
	case string:
		return t, "string"
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano), "datetime"
	case []byte:
		return base64.StdEncoding.EncodeToString(t), "bytestring"
	case *ua.NodeID:
		if t == nil {
			return nil, "nodeid"
		}
		return t.String(), "nodeid"
	case *ua.LocalizedText:
		if t == nil {
			return nil, "localizedtext"
		}
		return t.Text, "localizedtext"
	case *ua.QualifiedName:
		if t == nil {
			return nil, "qualifiedname"
		}
		return t.Name, "qualifiedname"
	case ua.StatusCode:
		return fmt.Sprintf("0x%08X", uint32(t)), "statuscode"
	case *ua.ExtensionObject:
		return fmt.Sprintf("%v", t), "extensionobject"
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		out := make([]any, rv.Len())
		elemType := "unknown"
		for i := 0; i < rv.Len(); i++ {
			out[i], elemType = EncodeValue(rv.Index(i).Interface())
		}
		return out, "array<" + elemType + ">"
	}
	return fmt.Sprintf("%v", v), "unknown:" + reflect.TypeOf(v).String()
}

func formatFloat(f float64, bits int) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return strconv.FormatFloat(f, 'g', -1, bits)
}

// ScaleInteger applies a decimal-point scaling to an integer raw value and
// returns the engineering value as a decimal string (e.g. 2135 with 1 decimal
// -> "213.5"). Non-integers are returned unchanged as nil.
func ScaleInteger(v any, decimals int) any {
	var n *big.Int
	switch t := v.(type) {
	case int64:
		n = big.NewInt(t)
	case uint64:
		n = new(big.Int).SetUint64(t)
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
