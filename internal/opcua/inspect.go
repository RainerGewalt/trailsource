package opcua

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"

	"github.com/RainerGewalt/trailsource/internal/config"
	"github.com/RainerGewalt/trailsource/internal/inventory"
)

// Inspection is the result of a one-shot browse without MQTT, journal or
// state: what the adapter would see on its first browse.
type Inspection struct {
	Endpoint    string           `json:"endpoint"`
	Session     sessionInfo      `json:"session"`
	Namespaces  []string         `json:"namespaces"`
	BrowseRoots []string         `json:"browse_roots"`
	Fingerprint string           `json:"fingerprint"`
	Truncated   bool             `json:"truncated"`
	Visited     int              `json:"visited"`
	Requests    int              `json:"requests"`
	Took        string           `json:"took"`
	Items       []inventory.Item `json:"items"`
}

// Inspect connects as configured, browses once and closes the session. It
// reads attributes and references only; it never writes and never subscribes.
func Inspect(ctx context.Context, cfg *config.Config) (*Inspection, error) {
	client, info, err := dial(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Close(closeCtx)
	}()
	roots := make([]*ua.NodeID, 0, len(cfg.Source.BrowseRoots))
	for _, r := range cfg.Source.BrowseRoots {
		nid, err := ua.ParseNodeID(r)
		if err != nil {
			return nil, fmt.Errorf("invalid browse root %q: %w", r, err)
		}
		roots = append(roots, nid)
	}
	nsCtx, cancelNS := context.WithTimeout(ctx, cfg.RequestTimeout())
	namespaces, err := client.NamespaceArray(nsCtx)
	cancelNS()
	if err != nil {
		namespaces = client.Namespaces()
	}
	browseCtx, cancel := context.WithTimeout(ctx, cfg.BrowseTimeout())
	defer cancel()
	started := time.Now()
	res, err := BrowseVariables(browseCtx, client, roots, cfg.Source.MaxDepth, cfg.Source.MaxNodes, namespaces)
	if err != nil {
		return nil, fmt.Errorf("browse: %w", err)
	}
	items := make([]inventory.Item, 0, len(res.Nodes))
	for _, n := range res.Nodes {
		items = append(items, n.Item)
	}
	return &Inspection{Endpoint: cfg.Source.Endpoint, Session: info, Namespaces: namespaces, BrowseRoots: cfg.Source.BrowseRoots,
		Fingerprint: inventory.Fingerprint(items), Truncated: res.Truncated, Visited: res.Visited, Requests: res.Requests,
		Took: time.Since(started).Round(time.Millisecond).String(), Items: items}, nil
}

// Endpoint is one entry of a server's endpoint list.
type Endpoint struct {
	URL            string   `json:"url"`
	SecurityPolicy string   `json:"security_policy"`
	SecurityMode   string   `json:"security_mode"`
	SecurityLevel  uint8    `json:"security_level"`
	UserTokens     []string `json:"user_tokens"`
	ApplicationURI string   `json:"application_uri,omitempty"`
	ProductURI     string   `json:"product_uri,omitempty"`
}

// Endpoints asks the server for its endpoint list (GetEndpoints, no session).
func Endpoints(ctx context.Context, url string) ([]Endpoint, error) {
	eps, err := opcua.GetEndpoints(ctx, url)
	if err != nil {
		return nil, err
	}
	out := make([]Endpoint, 0, len(eps))
	for _, ep := range eps {
		e := Endpoint{URL: ep.EndpointURL, SecurityPolicy: strings.TrimPrefix(ep.SecurityPolicyURI, ua.SecurityPolicyURIPrefix),
			SecurityMode: modeName(ep.SecurityMode), SecurityLevel: ep.SecurityLevel}
		if ep.Server != nil {
			e.ApplicationURI, e.ProductURI = ep.Server.ApplicationURI, ep.Server.ProductURI
		}
		for _, t := range ep.UserIdentityTokens {
			if t != nil {
				e.UserTokens = append(e.UserTokens, tokenName(t.TokenType))
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func modeName(m ua.MessageSecurityMode) string {
	switch m {
	case ua.MessageSecurityModeNone:
		return "None"
	case ua.MessageSecurityModeSign:
		return "Sign"
	case ua.MessageSecurityModeSignAndEncrypt:
		return "SignAndEncrypt"
	default:
		return fmt.Sprintf("Invalid(%d)", m)
	}
}

func tokenName(t ua.UserTokenType) string {
	switch t {
	case ua.UserTokenTypeAnonymous:
		return "anonymous"
	case ua.UserTokenTypeUserName:
		return "username"
	case ua.UserTokenTypeCertificate:
		return "certificate"
	case ua.UserTokenTypeIssuedToken:
		return "issued"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}
