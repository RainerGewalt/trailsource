package opcua

import (
	"testing"

	"github.com/gopcua/opcua/ua"
)

// Only the exact OPC UA names map to a mode; anything else is unknown and
// never an unsecured session (config.Load refuses it earlier as well).
func TestMessageSecurityModeIsExact(t *testing.T) {
	for in, want := range map[string]ua.MessageSecurityMode{
		"None": ua.MessageSecurityModeNone, "Sign": ua.MessageSecurityModeSign, "SignAndEncrypt": ua.MessageSecurityModeSignAndEncrypt,
	} {
		if got, ok := messageSecurityMode(in); !ok || got != want {
			t.Errorf("%s: got %v %v", in, got, ok)
		}
	}
	for _, in := range []string{"signandencrypt", "SignAndEncrpyt", "sign", "", "none"} {
		if got, ok := messageSecurityMode(in); ok || got == ua.MessageSecurityModeNone {
			t.Errorf("%q must be unknown, got %v %v", in, got, ok)
		}
	}
}
