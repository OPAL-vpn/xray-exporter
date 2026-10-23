package probe

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
)

// verifyPeer checks the peer chain against roots and then the leaf against
// name, which may be a DNS name or an address literal. Both checks matter: a
// chain that reaches a trusted root says nothing about who the certificate was
// issued to.
func verifyPeer(state tls.ConnectionState, roots *x509.CertPool, name string) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("no peer certificate")
	}

	leaf := state.PeerCertificates[0]
	intermediates := x509.NewCertPool()
	for _, cert := range state.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}

	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
	}); err != nil {
		return err
	}
	if name == "" {
		return errors.New("no name to verify the certificate against")
	}

	return leaf.VerifyHostname(name)
}
