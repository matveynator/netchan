// Package netchan provides functionalities related to network channels,
// focusing on secure communication using TLS. It includes utilities for generating
// self-signed certificates and configuring TLS for network communications.
package netchan

import (
	"crypto/ecdsa"     // Implements the Elliptic Curve Digital Signature Algorithm (ECDSA).
	"crypto/elliptic"  // Implements several standard elliptic curves.
	"crypto/rand"      // Provides functions for generating cryptographically secure random numbers.
	"crypto/tls"       // Implements the Transport Layer Security (TLS) and its predecessor, SSL.
	"crypto/x509"      // Parses and creates x509 certificates.
	"crypto/x509/pkix" // Contains shared, low level structures used for ASN.1 parsing and serialization of X.509 certificates.
	"encoding/pem"     // Implements PEM data encoding, used in TLS keys and certificates.
	"math/big"         // Provides multi-precision arithmetic (big numbers).
	"time"             // Provides functionality for measuring and displaying time.
)

// generateSelfSignedCert creates a self-signed certificate and corresponding private key.
// It returns the certificate and key in PEM format, along with any error encountered.
func generateSelfSignedCert() ([]byte, []byte, error) {
	// Generate an elliptic curve digital signature algorithm (ECDSA) private key.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	// Define a time range for which the certificate is valid.
	notBefore := time.Now()
	notAfter := notBefore.Add(50 * 365 * 24 * time.Hour) // 50 years validity

	// Generate a random serial number for the certificate.
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	// Create a certificate template with various settings.
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"github.com/matveynator/netchan"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	// Create the self-signed certificate using the template and private key.
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	// Encode the certificate to PEM format.
	certPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	// Marshal the ECDSA private key and encode it to PEM format.
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	return certPem, keyPem, nil
}

// generateTLSConfig creates a TLS configuration using the generated self-signed certificate.
// It returns the TLS configuration or an error if the certificate generation fails.
func generateTLSConfig() (*tls.Config, error) {
	// Generate a self-signed certificate and private key.
	certPEM, keyPEM, err := generateSelfSignedCert()
	if err != nil {
		return nil, err
	}

	// Create a TLS certificate using the PEM encoded certificate and key.
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	// Define the TLS configuration with the created certificate.
	// The configuration uses the most recent TLS version (TLS 1.3) and skips
	// client certificate verification (InsecureSkipVerify).
	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
	}

	return tlsConfig, nil
}
