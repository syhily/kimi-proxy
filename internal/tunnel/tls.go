package tunnel

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"time"
)

// tlsIdentity derives a deterministic Ed25519 key pair from a 32-byte seed
// (see DeriveTLSCertKey) and wraps it in a self-signed certificate. Both ends
// derive the same key pair from the shared token, so the peer is
// authenticated by comparing the certificate's public key — a MITM without
// the token cannot impersonate either side.
func tlsIdentity(seed []byte) (tls.Certificate, ed25519.PublicKey, error) {
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kimi-proxy"},
		NotBefore:             time.Unix(0, 0),
		NotAfter:              time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, pub, nil
}

// verifyPeer pins the peer's leaf certificate public key to want. It is used
// on both ends: the client pins the server, the server pins the client.
func verifyPeer(rawCerts [][]byte, want ed25519.PublicKey) error {
	if len(rawCerts) == 0 {
		return errors.New("tunnel: peer presented no certificate")
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return err
	}
	certPub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || !bytes.Equal(certPub, want) {
		return errors.New("tunnel: peer identity mismatch (wrong token?)")
	}
	return nil
}

// ListenTCP creates a TLS listener for TCP-only deployments where the KCP/UDP
// tunnel port cannot be exposed. The certificate identity is derived from the
// same shared token as the KCP cipher key, and the client is authenticated
// the same way: the connection is only accepted if the client presents a
// certificate carrying the same derived public key.
func ListenTCP(addr, token string) (net.Listener, error) {
	cert, pub, err := tlsIdentity(DeriveTLSCertKey(token))
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		// The certificate is self-signed, so chain verification is useless
		// here; RequireAnyClientCert only demands that a certificate was
		// presented, and verifyPeer below pins its public key.
		ClientAuth: tls.RequireAnyClientCert,
		MinVersion: tls.VersionTLS13,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyPeer(rawCerts, pub)
		},
	}
	return tls.Listen("tcp", addr, cfg)
}

// DialTCP connects to a ListenTCP peer. Both sides are pinned to the identity
// derived from the shared token: the client presents its own certificate
// (verified by the server) and verifies the server's certificate in return.
func DialTCP(addr, token string) (net.Conn, error) {
	cert, pub, err := tlsIdentity(DeriveTLSCertKey(token))
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, //nolint:gosec // identity is pinned below
		MinVersion:         tls.VersionTLS13,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyPeer(rawCerts, pub)
		},
	}
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, err
	}
	tuneTCP(conn)
	return conn, nil
}
