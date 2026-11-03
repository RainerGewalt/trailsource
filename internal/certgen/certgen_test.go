package certgen

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestGenerateHasApplicationURIAndUsages(t *testing.T) {
	res, err := Generate(Options{ApplicationURI: "urn:trailsource:host01:extruder-01", Hosts: []string{"host01", "10.0.0.5"}})
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(res.CertDER)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "urn:trailsource:host01:extruder-01" {
		t.Fatalf("URI SAN wrong: %v", cert.URIs)
	}
	if len(cert.DNSNames) != 1 || len(cert.IPAddresses) != 1 {
		t.Fatalf("host SANs wrong: %v %v", cert.DNSNames, cert.IPAddresses)
	}
	for _, ku := range []x509.KeyUsage{x509.KeyUsageDigitalSignature, x509.KeyUsageKeyEncipherment, x509.KeyUsageDataEncipherment} {
		if cert.KeyUsage&ku == 0 {
			t.Errorf("key usage %d missing", ku)
		}
	}
	block, _ := pem.Decode(res.KeyPEM)
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		t.Fatal("key is not PKCS#1 PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil || key.N.BitLen() != 2048 {
		t.Fatalf("key invalid: %v", err)
	}
	if len(res.Thumbprint) != 40 {
		t.Fatalf("thumbprint %q", res.Thumbprint)
	}
}

func TestGenerateRejectsMissingURI(t *testing.T) {
	if _, err := Generate(Options{}); err == nil {
		t.Fatal("missing URI accepted")
	}
	if _, err := Generate(Options{ApplicationURI: "not a uri"}); err == nil {
		t.Fatal("invalid URI accepted")
	}
}
