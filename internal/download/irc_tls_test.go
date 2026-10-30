package download

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

const testLegacyHost = "irc.irchighway.net"

func makeLegacyTestCertificate(t *testing.T, name string, dnsNames []string, expired bool) (*x509.CertPool, *x509.Certificate, tls.Certificate) {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Testing IRC CA"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IsCA:         true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(rootCert)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	notBefore := time.Now().Add(-time.Hour)
	notAfter := time.Now().Add(time.Hour)
	if expired {
		notBefore, notAfter = time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     dnsNames,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, rootCert, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	return roots, cert, tls.Certificate{Certificate: [][]byte{leafDER, rootDER}, PrivateKey: leafKey, Leaf: cert}
}

func TestLegacyCNOptInExactHostOnly(t *testing.T) {
	t.Setenv("AUTOFETCH_IRC_TLS_LEGACY_CN_HOSTS", "example.org, irc.irchighway.net")
	for _, host := range []string{"irc.irchighway.net", "IRC.IRCHIGHWAY.NET"} {
		if !legacyCNEnabled(host) {
			t.Fatalf("host %q should be opted in", host)
		}
	}
	for _, host := range []string{"evil.irchighway.net", "irc.irchighway.net.evil", "127.0.0.1", "example.net"} {
		if legacyCNEnabled(host) {
			t.Fatalf("host %q must not be opted in", host)
		}
	}
	t.Setenv("AUTOFETCH_IRC_TLS_LEGACY_CN_HOSTS", "")
	if cfg := ircTLSConfig(testLegacyHost); cfg.InsecureSkipVerify || cfg.VerifyConnection != nil {
		t.Fatal("default must use normal TLS verification")
	}
	t.Setenv("AUTOFETCH_IRC_TLS_LEGACY_CN_HOSTS", testLegacyHost)
	if cfg := ircTLSConfig(testLegacyHost); !cfg.InsecureSkipVerify || cfg.VerifyConnection == nil {
		t.Fatal("opt-in needs a strict custom verifier")
	}
}

func TestLegacyCNRequiresTrustedChainAndExactIdentity(t *testing.T) {
	roots, valid, _ := makeLegacyTestCertificate(t, testLegacyHost, nil, false)
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{valid}}
	if err := verifyIRCServerCertificate(state, testLegacyHost, roots); err != nil {
		t.Fatalf("valid, CA-trusted legacy CN rejected: %v", err)
	}

	tests := []struct {
		name       string
		cn         string
		dnsNames   []string
		expired    bool
		wrongRoots bool
	}{
		{name: "different CN", cn: "attacker.example.net"},
		{name: "SAN mismatch despite matching CN", cn: testLegacyHost, dnsNames: []string{"attacker.example.net"}},
		{name: "expired", cn: testLegacyHost, expired: true},
		{name: "untrusted CA", cn: testLegacyHost, wrongRoots: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			certRoots, cert, _ := makeLegacyTestCertificate(t, tc.cn, tc.dnsNames, tc.expired)
			if tc.wrongRoots {
				certRoots = x509.NewCertPool()
			}
			err := verifyIRCServerCertificate(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}, testLegacyHost, certRoots)
			if err == nil {
				t.Fatal("must reject invalid TLS certificate")
			}
		})
	}
	if err := verifyIRCServerCertificate(tls.ConnectionState{}, testLegacyHost, roots); err == nil {
		t.Fatal("missing peer must fail")
	}
}

func TestVerifiedSANStillWorksWithExplicitHostAllowlist(t *testing.T) {
	roots, cert, _ := makeLegacyTestCertificate(t, "other.example.org", []string{testLegacyHost}, false)
	err := verifyIRCServerCertificate(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}, testLegacyHost, roots)
	if err != nil {
		t.Fatalf("valid SAN must pass: %v", err)
	}
	if err = verifyIRCServerCertificate(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}, "wrong.example.net", roots); err == nil || !strings.Contains(err.Error(), "san_required") {
		t.Fatalf("SAN mismatch must fail, got %v", err)
	}
}

// End-to-end TLS handshake (localhost TCP, but the TLS hostname is the exact
// configured IRC hostname). Ensures the opt-in verifier actually runs, rather
// than accidentally relying only on direct unit calls.
func TestLegacyCNTLSHandshakeRequiresExplicitOptIn(t *testing.T) {
	roots, _, serverCert := makeLegacyTestCertificate(t, testLegacyHost, nil, false)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{serverCert}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for i := 0; i < 2; i++ {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			_ = c.(*tls.Conn).Handshake()
			_ = c.Close()
		}
	}()
	// Matching CN, trusted root, but no SAN: the default Go check MUST fail.
	t.Setenv("AUTOFETCH_IRC_TLS_LEGACY_CN_HOSTS", "")
	addr := listener.Addr().String()
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	c, err := tls.DialWithDialer(dialer, "tcp", addr, ircTLSConfigWithRoots(testLegacyHost, roots))
	if err == nil {
		c.Close()
		t.Fatal("default TLS must reject legacy CN")
	}
	if !strings.Contains(err.Error(), "legacy Common Name") {
		t.Fatalf("unexpected default TLS error: %v", err)
	}
	// Same certificate, exact explicit host allowlist, fully validated chain.
	t.Setenv("AUTOFETCH_IRC_TLS_LEGACY_CN_HOSTS", testLegacyHost)
	c, err = tls.DialWithDialer(dialer, "tcp", addr, ircTLSConfigWithRoots(testLegacyHost, roots))
	if err != nil {
		t.Fatalf("opt-in trusted CN handshake failed: %v", err)
	}
	_ = c.Close()
	<-serverDone
}
