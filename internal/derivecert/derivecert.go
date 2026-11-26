// Package derivecert rebuilds the certificate authority that Pomerium derives
// from its shared secret (the one behind tls_derive), so the broker can verify
// the config API's certificate.
//
// Adapted from github.com/pomerium/pomerium/pkg/derivecert (Apache-2.0,
// Copyright Pomerium, Inc.), trimmed to the CA and server certificates. It is
// copied rather than imported to keep the whole pomerium module out of go.mod.
// The derivation has to match Pomerium's byte for byte; TestCAMatchesPomerium
// pins it. If Pomerium ever changes it, verification fails closed.
package derivecert

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"time"

	"filippo.io/keygen"
	"golang.org/x/crypto/hkdf"
)

var (
	notBefore = time.Date(2022, time.December, 1, 0, 0, 0, 0, time.UTC)
	notAfter  = time.Date(2050, time.December, 1, 0, 0, 0, 0, time.UTC)
)

type readerType byte

const (
	readerTypeCAPrivateKey readerType = iota
	readerTypeCACertificate
	readerTypeServerPrivateKey
	readerTypeServerCertificate
	readerTypeSerialNumber
)

// CA is the certificate authority derived from a pre-shared key.
type CA struct {
	psk  []byte
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
}

// NewCA derives the CA for psk, the decoded shared secret. The same psk always
// yields the same key.
func NewCA(psk []byte) (*CA, error) {
	key, err := deriveKey(newReader(readerTypeCAPrivateKey, psk))
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	serial, err := newSerial(psk)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"Pomerium"}, CommonName: "Pomerium PSK CA"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(newReader(readerTypeCACertificate, psk), tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("create cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}
	return &CA{psk: psk, key: key, cert: cert}, nil
}

// Certificate returns the CA certificate.
func (ca *CA) Certificate() *x509.Certificate { return ca.cert }

// NewServerCert derives the server certificate Pomerium serves for domains.
func (ca *CA) NewServerCert(domains ...string) (tls.Certificate, error) {
	key, err := deriveKey(newReader(readerTypeServerPrivateKey, ca.psk, domains...))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("derive key: %w", err)
	}
	serial, err := newSerial(ca.psk, domains...)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"Pomerium"}},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     domains,
	}
	der, err := x509.CreateCertificate(newReader(readerTypeServerCertificate, ca.psk, domains...), tmpl, ca.cert, key.Public(), ca.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create cert: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func newReader(t readerType, psk []byte, domains ...string) io.Reader {
	var buf bytes.Buffer
	buf.WriteByte(byte(t))
	buf.Write(psk)
	buf.WriteByte(0)
	for _, d := range domains {
		buf.WriteString(d)
		buf.WriteByte(0)
	}
	return hkdf.New(sha256.New, buf.Bytes(), nil, nil)
}

func deriveKey(r io.Reader) (*ecdsa.PrivateKey, error) {
	return keygen.ECDSALegacy(elliptic.P256(), r)
}

func newSerial(psk []byte, domains ...string) (*big.Int, error) {
	n, err := rand.Int(newReader(readerTypeSerialNumber, psk, domains...), new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("serial number: %w", err)
	}
	return n, nil
}
