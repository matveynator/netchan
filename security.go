package netchan

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"
)

// generateSelfSignedCert creates a self-signed TLS certificate.
// It generates an ECDSA private key, creates a certificate template, and signs the certificate with its own key.
// It returns the PEM encoded certificate and private key, or an error in case of failure.
func generateSelfSignedCert() ([]byte, []byte, error) {
	// Generate an ECDSA private key using the P256 curve.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	// Define certificate validity period.
	notBefore := time.Now()
	notAfter := notBefore.Add(50 * 365 * 24 * time.Hour)

	// Generate a serial number for the certificate.
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	// Create a certificate template with basic details and settings.
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

	// Create the certificate using the template and private key.
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	// PEM encode the certificate.
	certPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	// Marshal and PEM encode the private key.
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	return certPem, keyPem, nil
}

// generateTLSConfig creates a TLS configuration using a self-signed certificate.
// It generates a certificate and key pair, creates a TLS configuration with these, and sets the minimum TLS version to 1.3.
// Returns the TLS configuration or an error in case of failure.
func generateTLSConfig() (*tls.Config, error) {
	// Generate self-signed certificate.
	certPEM, keyPEM, err := generateSelfSignedCert()
	if err != nil {
		return nil, err
	}

	// Load the certificate and key pair.
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	// Create and configure the TLS configuration.
	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // Note: InsecureSkipVerify should be used with caution.
	}

	return tlsConfig, nil
}
