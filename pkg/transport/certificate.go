package transport

import "crypto/x509"

func parseCertificate(encoded []byte) (*x509.Certificate, error) {
	return x509.ParseCertificate(encoded)
}
