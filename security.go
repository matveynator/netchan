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

// generateSelfSignedCert generates a self-signed certificate and private key.
func generateSelfSignedCert() ([]byte, []byte, error) {
	// Generate an ECDSA key (you could replace with RSA or another type if desired).
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	// Create a template for the self-signed certificate.
	notBefore := time.Now()
	notAfter := notBefore.Add(50 * 365 * 24 * time.Hour) // Certificate valid for 50 years

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

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

	// Create the certificate.
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	// Encode the certificate and key to PEM format.
	certPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	return certPem, keyPem, nil
}

// generateTLSConfig creates and returns a TLS configuration using the generated self-signed certificate.
func generateTLSConfig() (*tls.Config, error) {
	certPEM, keyPEM, err := generateSelfSignedCert()
	if err != nil {
		return nil, err
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		InsecureSkipVerify: true,
	}

	return tlsConfig, nil
}
