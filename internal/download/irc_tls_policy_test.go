package download

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestIRCPolicyValidation(t *testing.T) {
	pin := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, tc := range []struct {
		tls       bool
		mode, pin string
		valid     bool
	}{
		{true, "", "", true}, {true, "strict", "", true}, {true, "unverified", "", true},
		{true, "pinned", pin, true}, {false, "unverified", "", false}, {true, "pinned", "a", false},
		{true, "bad", "", false}, {true, "strict", pin, false}, {true, "unverified", pin, false},
	} {
		err := validateIRCTLSPolicy(tc.tls, tc.mode, tc.pin)
		if (err == nil) != tc.valid {
			t.Errorf("mode=%q valid=%v err=%v", tc.mode, tc.valid, err)
		}
	}
}
func TestIRCPinnedTLSVerifiesKeyAndValidity(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(42), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Minute), KeyUsage: x509.KeyUsageDigitalSignature}
	raw, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(spki)
	pin := base64.StdEncoding.EncodeToString(hash[:])
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	cfg := ircTLSConfigForPolicy("irc.example", "pinned", pin)
	if err := cfg.VerifyConnection(state); err != nil {
		t.Fatal(err)
	}
	wrong := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := ircTLSConfigForPolicy("irc.example", "pinned", wrong).VerifyConnection(state); err == nil || !strings.Contains(err.Error(), "pin_mismatch") {
		t.Fatalf("wrong pin: %v", err)
	}
	expired := *cert
	expired.NotAfter = now.Add(-time.Second)
	if err := cfg.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{&expired}}); err == nil {
		t.Fatal("expired accepted")
	}
	if err := cfg.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Fatal("missing cert accepted")
	}
}
