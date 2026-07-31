package tunnel

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"time"
)

// tlsIdentity derives a deterministic Ed25519 key pair from the tunnel key
// and wraps it in a self-signed certificate. Both ends derive the same key
// pair from the shared token, so the peer is authenticated by comparing the
// certificate's public key — a MITM without the token cannot impersonate
// either side.
func tlsIdentity(key []byte) (tls.Certificate, ed25519.PublicKey, error) {
	seed := sha256.Sum256(append([]byte("kimi-proxy/tls:"), key...))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kimi-proxy"},
		NotBefore:             time.Unix(0, 0),
		NotAfter:              time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, pub, nil
}

// ListenTCP creates a TLS listener for TCP-only deployments where the KCP/UDP
// tunnel port cannot be exposed. The certificate identity is derived from the
// same shared token as the KCP cipher key.
func ListenTCP(addr string, key []byte) (net.Listener, error) {
	cert, _, err := tlsIdentity(key)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
	return tls.Listen("tcp", addr, cfg)
}

// DialTCP connects to a ListenTCP peer and verifies that its certificate
// carries the public key derived from the shared token.
func DialTCP(addr string, key []byte) (net.Conn, error) {
	_, pub, err := tlsIdentity(key)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // identity is pinned below
		MinVersion:         tls.VersionTLS13,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("tunnel: server presented no certificate")
			}
			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			certPub, ok := cert.PublicKey.(ed25519.PublicKey)
			if !ok || !bytes.Equal(certPub, pub) {
				return errors.New("tunnel: server identity mismatch (wrong token?)")
			}
			return nil
		},
	}
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, err
	}
	if tc, ok := conn.NetConn().(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	return conn, nil
}
