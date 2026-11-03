package opcua

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/gopcua/opcua/ua"
)

// A browse on a dying session must abort instead of sealing an empty or
// partial inventory; only errors about one node may be skipped.
func TestIsConnectionError(t *testing.T) {
	abort := []error{
		ua.StatusBadServerNotConnected,
		ua.StatusBadConnectionClosed,
		ua.StatusBadSessionIDInvalid,
		ua.StatusBadSecureChannelClosed,
		ua.StatusBadTimeout,
		fmt.Errorf("read: %w", ua.StatusBadServerNotConnected),
		io.EOF,
		context.DeadlineExceeded,
		errors.New("dial tcp 127.0.0.1:4840: connect: connection refused"),
	}
	for _, err := range abort {
		if !IsConnectionError(err) {
			t.Errorf("%v: want connection error (abort browse)", err)
		}
	}
	skip := []error{
		ua.StatusBadNodeIDUnknown,
		ua.StatusBadUserAccessDenied,
		fmt.Errorf("attrs: %w", ua.StatusBadNotReadable),
	}
	for _, err := range skip {
		if IsConnectionError(err) {
			t.Errorf("%v: want node-level error (skip node)", err)
		}
	}
	if IsConnectionError(nil) {
		t.Error("nil is not an error")
	}
}
