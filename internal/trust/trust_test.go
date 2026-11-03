package trust

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

const serverURI = "urn:example:server"

type issued struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
}

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// issue creates a certificate; parent nil means self-signed.
func issue(t *testing.T, serial int64, cn string, isCA bool, uri string, notAfter time.Time, parent *issued) issued {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn},
		NotBefore: now.Add(-24 * time.Hour), NotAfter: notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment,
		BasicConstraintsValid: true, IsCA: isCA,
	}
	if isCA {
		tmpl.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}
	if uri != "" {
		u, _ := url.Parse(uri)
		tmpl.URIs = []*url.URL{u}
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return issued{c, key}
}

func crl(t *testing.T, ca issued, nextUpdate time.Time, revoked ...*big.Int) []byte {
	t.Helper()
	var entries []x509.RevocationListEntry
	for _, s := range revoked {
		entries = append(entries, x509.RevocationListEntry{SerialNumber: s, RevocationTime: now.Add(-time.Hour)})
	}
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number: big.NewInt(1), ThisUpdate: now.Add(-time.Hour), NextUpdate: nextUpdate, RevokedCertificateEntries: entries,
	}, ca.cert, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func store(t *testing.T) Store {
	t.Helper()
	s := Store{Dir: t.TempDir(), Now: func() time.Time { return now }}
	if err := s.Ensure(); err != nil {
		t.Fatal(err)
	}
	return s
}

func put(t *testing.T, s Store, sub, name string, der []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Dir, sub, name), der, 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantCheck(t *testing.T, err error, check string) *Rejection {
	t.Helper()
	var r *Rejection
	if !errors.As(err, &r) || r.Check != check {
		t.Fatalf("want rejection %s, got %v", check, err)
	}
	return r
}

func TestSelfSignedServerCertificate(t *testing.T) {
	s := store(t)
	srv := issue(t, 1, "server", false, serverURI, now.Add(365*24*time.Hour), nil)

	// Empty trust list: rejected, copied to rejected/ for review.
	r := wantCheck(t, func() error { _, err := s.Check(srv.cert.Raw, serverURI); return err }(), CheckUntrusted)
	if r.RejectedFile != filepath.Join(s.Dir, "rejected", Thumbprint(srv.cert.Raw)+".der") {
		t.Fatalf("rejected file %q", r.RejectedFile)
	}
	if _, err := os.Stat(r.RejectedFile); err != nil {
		t.Fatal(err)
	}

	// The administrator moves it to trusted/certs: accepted.
	if err := os.Rename(r.RejectedFile, filepath.Join(s.Dir, "trusted", "certs", filepath.Base(r.RejectedFile))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(srv.cert.Raw, serverURI); err != nil {
		t.Fatalf("trusted certificate rejected: %v", err)
	}
}

func TestValidityAndURI(t *testing.T) {
	s := store(t)
	expired := issue(t, 2, "old", false, serverURI, now.Add(-time.Hour), nil)
	put(t, s, "trusted/certs", "old.der", expired.cert.Raw)
	_, err := s.Check(expired.cert.Raw, serverURI)
	wantCheck(t, err, CheckExpired)

	other := issue(t, 3, "other", false, "urn:example:other", now.Add(time.Hour), nil)
	put(t, s, "trusted/certs", "other.der", other.cert.Raw)
	_, err = s.Check(other.cert.Raw, serverURI)
	wantCheck(t, err, CheckURIMismatch)
}

func TestCAChainWithCRL(t *testing.T) {
	s := store(t)
	ca := issue(t, 10, "plant CA", true, "", now.Add(10*365*24*time.Hour), nil)
	srv := issue(t, 11, "server", false, serverURI, now.Add(365*24*time.Hour), &ca)
	put(t, s, "trusted/certs", "ca.der", ca.cert.Raw)

	// CA trusted but no CRL: revocation status unknown, rejected.
	_, err := s.Check(srv.cert.Raw, serverURI)
	wantCheck(t, err, CheckRevocationUnknown)

	// Current CRL that does not list the server: accepted.
	put(t, s, "trusted/crl", "ca.crl", crl(t, ca, now.Add(24*time.Hour)))
	if _, err := s.Check(srv.cert.Raw, serverURI); err != nil {
		t.Fatalf("valid chain rejected: %v", err)
	}

	// Revoked.
	put(t, s, "trusted/crl", "ca.crl", crl(t, ca, now.Add(24*time.Hour), srv.cert.SerialNumber))
	_, err = s.Check(srv.cert.Raw, serverURI)
	wantCheck(t, err, CheckRevoked)

	// Outdated CRL does not count.
	put(t, s, "trusted/crl", "ca.crl", crl(t, ca, now.Add(-time.Minute)))
	_, err = s.Check(srv.cert.Raw, serverURI)
	wantCheck(t, err, CheckRevocationUnknown)
}

func TestIntermediateFromIssuers(t *testing.T) {
	s := store(t)
	root := issue(t, 20, "root", true, "", now.Add(10*365*24*time.Hour), nil)
	inter := issue(t, 21, "intermediate", true, "", now.Add(5*365*24*time.Hour), &root)
	srv := issue(t, 22, "server", false, serverURI, now.Add(365*24*time.Hour), &inter)
	put(t, s, "trusted/certs", "root.der", root.cert.Raw)
	put(t, s, "trusted/crl", "root.crl", crl(t, root, now.Add(24*time.Hour)))
	put(t, s, "issuers/crl", "inter.crl", crl(t, inter, now.Add(24*time.Hour)))

	_, err := s.Check(srv.cert.Raw, serverURI)
	wantCheck(t, err, CheckUntrusted) // intermediate unknown

	put(t, s, "issuers/certs", "inter.der", inter.cert.Raw)
	if _, err := s.Check(srv.cert.Raw, serverURI); err != nil {
		t.Fatalf("chain through issuers/ rejected: %v", err)
	}
	// An issuer alone is not trusted.
	s2 := store(t)
	put(t, s2, "issuers/certs", "inter.der", inter.cert.Raw)
	_, err = s2.Check(srv.cert.Raw, serverURI)
	wantCheck(t, err, CheckUntrusted)
}

func TestGarbageAndEmpty(t *testing.T) {
	s := store(t)
	_, err := s.Check(nil, serverURI)
	wantCheck(t, err, CheckNoCertificate)
	_, err = s.Check([]byte("not a certificate"), serverURI)
	wantCheck(t, err, CheckUnparsable)
}
