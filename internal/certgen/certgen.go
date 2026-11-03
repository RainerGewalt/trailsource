// Package certgen creates a self-signed OPC UA application instance
// certificate for the adapter: RSA 2048, SHA-256, the ApplicationURI as URI
// SAN and the key usages OPC UA servers check. Servers must still be told to
// trust it; generating a certificate grants nothing by itself.
package certgen

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // OPC UA thumbprints are SHA-1 by specification
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"time"
)

// Options describe the certificate.
type Options struct {
	ApplicationURI string    // required, e.g. urn:trailsource:host01:extruder-01
	CommonName     string    // defaults to "TrailSource"
	Hosts          []string  // DNS names or IP addresses for the SAN
	Days           int       // validity, defaults to 730
	Now            time.Time // defaults to time.Now()
}

// Result holds the DER certificate (the form most servers import into their
// trust list), the PKCS#1 PEM private key and the SHA-1 thumbprint servers
// display in their rejected-certificates list.
type Result struct {
	CertDER    []byte
	KeyPEM     []byte
	Thumbprint string
}

// Generate creates a key pair and a self-signed certificate.
func Generate(o Options) (*Result, error) {
	if o.ApplicationURI == "" {
		return nil, errors.New("application URI is required")
	}
	uri, err := url.Parse(o.ApplicationURI)
	if err != nil || uri.Scheme == "" {
		return nil, errors.New("application URI must be a URI such as urn:trailsource:host:adapter")
	}
	if o.CommonName == "" {
		o.CommonName = "TrailSource"
	}
	if o.Days <= 0 {
		o.Days = 730
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: o.CommonName, Organization: []string{"TrailSource"}},
		NotBefore:    o.Now.Add(-time.Hour),
		NotAfter:     o.Now.Add(time.Duration(o.Days) * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment | x509.KeyUsageKeyEncipherment |
			x509.KeyUsageDataEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{uri},
	}
	for _, h := range o.Hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum(der) //nolint:gosec
	return &Result{
		CertDER:    der,
		KeyPEM:     pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		Thumbprint: hex.EncodeToString(sum[:]),
	}, nil
}
