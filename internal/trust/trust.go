// Package trust checks OPC UA server certificates against a trust list kept
// in the directory layout OPC UA applications use:
//
//	<pki>/trusted/certs   certificates trusted directly (self-signed server
//	                      certificates) and trusted CA certificates
//	<pki>/trusted/crl     revocation lists of the trusted CAs
//	<pki>/issuers/certs   intermediate CA certificates, not trusted on their own
//	<pki>/issuers/crl     revocation lists of those CAs
//	<pki>/rejected        certificates that failed the check, for review
//
// A certificate is accepted when it is within its validity period, carries
// the server's ApplicationURI, and is either in trusted/certs itself or
// chains to a CA in trusted/certs whose current CRL (and that of every CA on
// the way) does not list it. Anything else is rejected and the certificate
// is copied to rejected/, from where an administrator moves it to
// trusted/certs after checking it. Nothing is accepted silently.
package trust

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // OPC UA thumbprints are SHA-1 by specification
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Check names the reason a certificate was rejected.
const (
	CheckUnparsable        = "unparsable"
	CheckNotYetValid       = "not_yet_valid"
	CheckExpired           = "expired"
	CheckURIMismatch       = "uri_mismatch"
	CheckUntrusted         = "untrusted"
	CheckRevoked           = "revoked"
	CheckRevocationUnknown = "revocation_unknown"
	CheckPinMismatch       = "pin_mismatch"
	CheckNoCertificate     = "no_certificate"
)

// Rejection is the error returned for a rejected certificate.
type Rejection struct {
	Check        string
	Detail       string
	Thumbprint   string // SHA-1, lowercase hex
	Subject      string
	NotBefore    time.Time
	NotAfter     time.Time
	RejectedFile string // where the certificate was copied, if anywhere
}

func (r *Rejection) Error() string {
	msg := fmt.Sprintf("server certificate rejected (%s): %s", r.Check, r.Detail)
	if r.Thumbprint != "" {
		msg += ", thumbprint " + r.Thumbprint
	}
	if r.RejectedFile != "" {
		msg += ", copied to " + r.RejectedFile
	}
	return msg
}

// Store is a trust list directory.
type Store struct {
	Dir string
	Now func() time.Time // for tests; time.Now when nil
}

// Dirs lists the directories of the layout, relative to the store.
var Dirs = []string{"trusted/certs", "trusted/crl", "issuers/certs", "issuers/crl", "rejected"}

// Ensure creates the directory layout.
func (s Store) Ensure() error {
	for _, d := range Dirs {
		if err := os.MkdirAll(filepath.Join(s.Dir, d), 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Thumbprint returns the SHA-1 thumbprint of a DER certificate.
func Thumbprint(der []byte) string {
	sum := sha1.Sum(der) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// Check validates the certificate (or chain, leaf first, as OPC UA servers
// send it) presented by a server whose ApplicationURI is applicationURI.
// It returns the leaf on success and a *Rejection otherwise.
func (s Store) Check(chainDER []byte, applicationURI string) (*x509.Certificate, error) {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if len(chainDER) == 0 {
		return nil, &Rejection{Check: CheckNoCertificate, Detail: "the endpoint offers no server certificate"}
	}
	certs, err := x509.ParseCertificates(chainDER)
	if err != nil || len(certs) == 0 {
		return nil, s.reject(&Rejection{Check: CheckUnparsable, Detail: fmt.Sprint(err), Thumbprint: Thumbprint(chainDER)}, chainDER)
	}
	leaf := certs[0]
	rej := func(check, detail string) error {
		return s.reject(&Rejection{Check: check, Detail: detail, Thumbprint: Thumbprint(leaf.Raw), Subject: leaf.Subject.String(),
			NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}, leaf.Raw)
	}
	if now.Before(leaf.NotBefore) {
		return nil, rej(CheckNotYetValid, "valid from "+leaf.NotBefore.UTC().Format(time.RFC3339))
	}
	if now.After(leaf.NotAfter) {
		return nil, rej(CheckExpired, "expired "+leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if applicationURI != "" && !hasURI(leaf, applicationURI) {
		return nil, rej(CheckURIMismatch, fmt.Sprintf("certificate URIs %v do not contain the server's ApplicationURI %s", leaf.URIs, applicationURI))
	}

	trusted, err := loadCerts(filepath.Join(s.Dir, "trusted", "certs"))
	if err != nil {
		return nil, err
	}
	for _, t := range trusted {
		if bytes.Equal(t.Raw, leaf.Raw) {
			return leaf, nil // trusted as it is (the usual case: a self-signed server certificate)
		}
	}

	issuers, err := loadCerts(filepath.Join(s.Dir, "issuers", "certs"))
	if err != nil {
		return nil, err
	}
	roots, inter := x509.NewCertPool(), x509.NewCertPool()
	for _, t := range trusted {
		roots.AddCert(t)
	}
	for _, c := range append(issuers, certs[1:]...) {
		inter.AddCert(c)
	}
	chains, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if err != nil || len(chains) == 0 {
		return nil, rej(CheckUntrusted, "neither the certificate nor an issuer is in trusted/certs")
	}
	crls, err := loadCRLs(filepath.Join(s.Dir, "trusted", "crl"), filepath.Join(s.Dir, "issuers", "crl"))
	if err != nil {
		return nil, err
	}
	chain := chains[0]
	for i := 0; i+1 < len(chain); i++ {
		cert, issuer := chain[i], chain[i+1]
		status, detail := revocation(cert, issuer, crls, now)
		switch status {
		case CheckRevoked:
			return nil, rej(CheckRevoked, fmt.Sprintf("%s is revoked by %s", cert.Subject, issuer.Subject))
		case CheckRevocationUnknown:
			return nil, rej(CheckRevocationUnknown, detail)
		}
	}
	return leaf, nil
}

// revocation checks cert against the CRLs of issuer: revoked, unknown (no
// current CRL signed by the issuer), or "" when not revoked.
func revocation(cert, issuer *x509.Certificate, crls []*x509.RevocationList, now time.Time) (string, string) {
	found := false
	for _, crl := range crls {
		if !bytes.Equal(crl.RawIssuer, issuer.RawSubject) || crl.CheckSignatureFrom(issuer) != nil {
			continue
		}
		if !crl.NextUpdate.IsZero() && now.After(crl.NextUpdate) {
			continue // outdated: does not count as an answer
		}
		found = true
		for _, rc := range crl.RevokedCertificateEntries {
			if rc.SerialNumber.Cmp(cert.SerialNumber) == 0 {
				return CheckRevoked, ""
			}
		}
	}
	if !found {
		return CheckRevocationUnknown, fmt.Sprintf("no current CRL of %s in trusted/crl or issuers/crl", issuer.Subject)
	}
	return "", ""
}

func hasURI(c *x509.Certificate, uri string) bool {
	for _, u := range c.URIs {
		if u.String() == uri {
			return true
		}
	}
	return false
}

// reject copies the certificate to rejected/<thumbprint>.der.
func (s Store) reject(r *Rejection, der []byte) *Rejection {
	dir := filepath.Join(s.Dir, "rejected")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.Detail += "; could not create rejected/: " + err.Error()
		return r
	}
	path := filepath.Join(dir, r.Thumbprint+".der")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, der, 0o644); err != nil {
			r.Detail += "; could not write to rejected/: " + err.Error()
			return r
		}
	}
	r.RejectedFile = path
	return r
}

// loadCerts reads every DER or PEM certificate in dir (missing dir: none).
func loadCerts(dir string) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	err := eachFile(dir, func(name string, raw []byte) error {
		for _, der := range ders(raw, "CERTIFICATE") {
			certs, err := x509.ParseCertificates(der)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			out = append(out, certs...)
		}
		return nil
	})
	return out, err
}

func loadCRLs(dirs ...string) ([]*x509.RevocationList, error) {
	var out []*x509.RevocationList
	for _, dir := range dirs {
		err := eachFile(dir, func(name string, raw []byte) error {
			for _, der := range ders(raw, "X509 CRL") {
				crl, err := x509.ParseRevocationList(der)
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				out = append(out, crl)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func eachFile(dir string, fn func(string, []byte) error) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := fn(path, raw); err != nil {
			return err
		}
	}
	return nil
}

// ders returns the DER blocks of a PEM file of the given type, or the file
// itself when it is not PEM.
func ders(raw []byte, pemType string) [][]byte {
	var out [][]byte
	rest := raw
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == pemType {
			out = append(out, block.Bytes)
		}
	}
	if len(out) == 0 && !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("-----")) {
		out = append(out, raw)
	}
	return out
}
