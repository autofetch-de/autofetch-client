package download

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

// tlsSANOID identifies the subjectAltName extension; a legacy CN exception
// must never override a SAN, even one without DNS or IP entries.
var tlsSANOID = asn1.ObjectIdentifier{2, 5, 29, 17}

// legacyCNEnabled is a historic exact-host local opt-in for CA-verified
// certificates without SAN. It never applies to server-selected TLS policies.
func legacyCNEnabled(host string) bool {
	if net.ParseIP(host) != nil {
		return false
	}
	for _, item := range strings.Split(os.Getenv("AUTOFETCH_IRC_TLS_LEGACY_CN_HOSTS"), ",") {
		item = strings.TrimSpace(item)
		if item != "" && strings.EqualFold(item, host) {
			return true
		}
	}
	return false
}

func ircTLSConfig(host string) *tls.Config {
	return ircTLSConfigWithRoots(host, nil)
}

// root override exists for isolated certificate tests; production uses system roots.
func ircTLSConfigWithRoots(host string, roots *x509.CertPool) *tls.Config {
	cfg := &tls.Config{ServerName: host, RootCAs: roots}
	if !legacyCNEnabled(host) {
		return cfg // normal Go/OS trust and SAN validation
	}
	// Go's TLS stack does not support legacy-CN verification natively.
	// Manual verification is REQUIRED when the exception is enabled. It checks
	// CA trust, EKU, dates, hostname, and refuses SAN mismatches. This is not
	// an option to accept unverified or self-signed certificates.
	cfg.InsecureSkipVerify = true // replaced by strict VerifyConnection below
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		return verifyIRCServerCertificate(cs, host, roots)
	}
	return cfg
}

func verifyIRCServerCertificate(cs tls.ConnectionState, host string, roots *x509.CertPool) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("irc_tls_no_peer_certificate")
	}
	leaf := cs.PeerCertificates[0]
	intermediates := x509.NewCertPool()
	for _, cert := range cs.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}
	opts := x509.VerifyOptions{
		DNSName:       host,
		Roots:         roots, // nil uses system root store
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	// Prefer standard chain and SAN verification in every case.
	_, verifyErr := leaf.Verify(opts)
	if verifyErr == nil {
		return nil
	}

	hostnameErr := leaf.VerifyHostname(host)
	if hostnameErr == nil {
		return fmt.Errorf("irc_tls_verification_failed: %w", verifyErr)
	}
	var nameFailure x509.HostnameError
	if !errors.As(hostnameErr, &nameFailure) {
		return fmt.Errorf("irc_tls_verification_failed: %w", verifyErr)
	}
	for _, ext := range leaf.Extensions {
		if ext.Id.Equal(tlsSANOID) {
			return fmt.Errorf("irc_tls_san_required: %w", verifyErr)
		}
	}
	if net.ParseIP(host) != nil || host == "" || strings.Contains(host, "*") ||
		!strings.EqualFold(leaf.Subject.CommonName, host) {
		return fmt.Errorf("irc_tls_common_name_mismatch: %w", hostnameErr)
	}
	// A matching old-style CN is not enough: normal trust-chain, validity,
	// key usage checks from x509.Verify still apply.
	opts.DNSName = ""
	if _, err := leaf.Verify(opts); err != nil {
		return fmt.Errorf("irc_tls_legacy_cn_chain_invalid: %w", err)
	}
	log.Printf("IRC TLS: accepted CA-verified legacy CN certificate for explicitly allowed host %s (no SAN)", host)
	return nil
}

// validateIRCTLSPolicy rejects malformed instructions before opening any socket.
func validateIRCTLSPolicy(useTLS bool, mode, pin string) error {
	if mode == "" {
		mode = "strict"
	}
	if !useTLS && mode != "strict" {
		return fmt.Errorf("irc_tls_policy_requires_tls")
	}
	switch mode {
	case "strict":
		if pin != "" {
			return fmt.Errorf("irc_tls_unexpected_pin")
		}
	case "unverified":
		if pin != "" {
			return fmt.Errorf("irc_tls_unexpected_pin")
		}
	case "pinned":
		b, err := base64.StdEncoding.Strict().DecodeString(pin)
		if err != nil || len(b) != sha256.Size || base64.StdEncoding.EncodeToString(b) != pin {
			return fmt.Errorf("irc_tls_invalid_spki_pin")
		}
	default:
		return fmt.Errorf("irc_tls_unknown_verification_mode")
	}
	return nil
}

func ircTLSConfigForPolicy(host, mode, pin string) *tls.Config {
	if mode == "" || mode == "strict" {
		return ircTLSConfig(host)
	}
	if mode == "unverified" {
		// Only called for a validated, explicitly server-selected policy; authentication
		// credentials must be rejected before entering this TLS session.
		return &tls.Config{ServerName: host, InsecureSkipVerify: true} //nolint:gosec
	}
	return &tls.Config{ServerName: host, InsecureSkipVerify: true, //nolint:gosec
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("irc_tls_pin_missing_certificate")
			}
			cert := cs.PeerCertificates[0]
			now := time.Now()
			if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return fmt.Errorf("irc_tls_pin_certificate_expired_or_not_yet_valid")
			}
			der, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
			if err != nil {
				return fmt.Errorf("irc_tls_pin_invalid_key: %w", err)
			}
			sum := sha256.Sum256(der)
			expected, err := base64.StdEncoding.Strict().DecodeString(pin)
			if err != nil || len(expected) != len(sum) || subtle.ConstantTimeCompare(sum[:], expected) != 1 {
				return fmt.Errorf("irc_tls_pin_mismatch")
			}
			return nil
		}}
}
