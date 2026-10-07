/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package extmetrics

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Validity and renewal (§9.4).
const (
	CAValidity      = 10 * 365 * 24 * time.Hour
	ServingValidity = 365 * 24 * time.Hour
	RenewBefore     = 30 * 24 * time.Hour
)

// The Secret's CA keys, beside corev1.TLSCertKey and TLSPrivateKeyKey.
const (
	KeyCACert = "ca.crt"
	KeyCAKey  = "ca.key"
)

// SANs are the names the aggregator may dial the Service by.
func SANs(service, namespace string) []string {
	return []string{
		service,
		service + "." + namespace,
		service + "." + namespace + ".svc",
		service + "." + namespace + ".svc.cluster.local",
	}
}

// NewCA makes an ECDSA P-256 CA valid for CAValidity from now.
func NewCA(now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "clustarr-external-metrics-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("extmetrics: create the CA: %w", err)
	}
	return encode(der, key)
}

// IssueServing signs a serving certificate for the Service, valid for
// ServingValidity from now.
func IssueServing(caCertPEM, caKeyPEM []byte, service, namespace string, now time.Time) (certPEM, keyPEM []byte, err error) {
	ca, caKey, err := parsePair(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: service + "." + namespace + ".svc"},
		DNSNames:     SANs(service, namespace),
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(ServingValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("extmetrics: sign the serving certificate: %w", err)
	}
	return encode(der, key)
}

// RenewalReason says why the serving certificate must be re-issued, or ""
// when it is good: unparseable, under RenewBefore left, other names than
// SANs, or not signed by the CA.
func RenewalReason(caCertPEM, certPEM []byte, service, namespace string, now time.Time) string {
	leaf, err := parseCert(certPEM)
	if err != nil {
		return "the serving certificate does not parse"
	}
	if leaf.NotAfter.Sub(now) < RenewBefore {
		return fmt.Sprintf("the serving certificate expires at %s", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if !slices.Equal(leaf.DNSNames, SANs(service, namespace)) {
		return "the serving certificate names another Service"
	}
	ca, err := parseCert(caCertPEM)
	if err != nil {
		return "the CA does not parse"
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now}); err != nil {
		return "the serving certificate is not the CA's"
	}
	return ""
}

// CANeedsRenewal is true when the CA or its key is missing or unparseable,
// the key is not the certificate's, or under RenewBefore is left.
func CANeedsRenewal(caCertPEM, caKeyPEM []byte, now time.Time) bool {
	ca, _, err := parsePair(caCertPEM, caKeyPEM)
	return err != nil || ca.NotAfter.Sub(now) < RenewBefore
}

// NewSecretData is a fresh CA and serving pair, keyed as the Secret holds
// them.
func NewSecretData(service, namespace string, now time.Time) (map[string][]byte, error) {
	caCert, caKey, err := NewCA(now)
	if err != nil {
		return nil, err
	}
	cert, key, err := IssueServing(caCert, caKey, service, namespace, now)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key,
		KeyCACert: caCert, KeyCAKey: caKey,
	}, nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

func encode(der []byte, key *ecdsa.PrivateKey) (certPEM, keyPEM []byte, err error) {
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), nil
}

func parseCert(certPEM []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("extmetrics: no certificate PEM block")
	}
	return x509.ParseCertificate(b.Bytes)
}

func parsePair(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, nil, err
	}
	b, _ := pem.Decode(keyPEM)
	if b == nil {
		return nil, nil, errors.New("extmetrics: no key PEM block")
	}
	key, err := x509.ParseECPrivateKey(b.Bytes)
	if err != nil {
		return nil, nil, err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, nil, errors.New("extmetrics: the key is not the certificate's")
	}
	if !cert.IsCA || !bytes.Equal(cert.RawIssuer, cert.RawSubject) {
		return nil, nil, errors.New("extmetrics: not a self-signed CA")
	}
	return cert, key, nil
}
