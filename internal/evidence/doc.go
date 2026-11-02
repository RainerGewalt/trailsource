// Package evidence implements the Trail Evidence Record v1 format: RFC 8785
// (JCS) canonicalisation, SHA-256 content and chain hashes, record sealing
// and stream verification.
//
// TODO(trail-evidence): replace this package with
// github.com/RainerGewalt/trail-evidence/go once that library is published.
// The behaviour here follows the binding v1 draft byte for byte; the test
// vectors in testdata/vectors.json are the contract both sides must meet.
package evidence
