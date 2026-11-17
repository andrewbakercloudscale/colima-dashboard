package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testCert(t *testing.T, cn string, ca bool, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: notAfter.Add(-48 * time.Hour), NotAfter: notAfter, IsCA: ca, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCABundle(t *testing.T) {
	now := time.Now()
	root := testCert(t, "Corp Root", true, now.Add(time.Hour))
	in := string(root) + string(root) + // a duplicate
		string(testCert(t, "leaf.example", false, now.Add(time.Hour))) + // not a CA
		string(testCert(t, "Old Root", true, now.Add(-time.Hour))) // expired
	bundle, names := caBundle([]byte(in), now)
	if strings.Join(names, ",") != "Corp Root" {
		t.Fatalf("names = %v", names)
	}
	if strings.Count(string(bundle), "BEGIN CERTIFICATE") != 1 {
		t.Fatalf("bundle has %d certificates", strings.Count(string(bundle), "BEGIN CERTIFICATE"))
	}
}

func TestSummarize(t *testing.T) {
	if got := summarize([]string{"a", "b", "c"}, 2); got != "a, b and 1 more" {
		t.Fatal(got)
	}
}

func TestParseCurlCode(t *testing.T) {
	for in, want := range map[string]int{"curl=0\n": 0, "noise\ncurl=60\n": 60} {
		if got, err := parseCurlCode(in); err != nil || got != want {
			t.Errorf("parseCurlCode(%q) = %d, %v", in, got, err)
		}
	}
	if _, err := parseCurlCode("nothing"); err == nil {
		t.Error("want an error")
	}
}
