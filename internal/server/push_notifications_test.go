package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestNewAPNSClientAcceptsBase64AndRawPEMKeys(t *testing.T) {
	pemKey := testAPNSPrivateKeyPEM(t)
	encoded := base64.StdEncoding.EncodeToString(pemKey)
	spacedEncoded := encoded[:16] + "\n\t" + encoded[16:]

	tests := []struct {
		name  string
		value string
	}{
		{name: "base64 PEM", value: spacedEncoded},
		{name: "raw PEM", value: string(pemKey)},
		{name: "escaped newline PEM", value: strings.ReplaceAll(string(pemKey), "\n", `\n`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewAPNSClient(&config.Env{
				APNSKeyID:       "KEYID12345",
				APNSTeamID:      "TEAMID1234",
				APNSBundleID:    "app.rxlab.podcast",
				APNSKeyBase64:   tt.value,
				APNSEnvironment: PushEnvironmentProduction,
			})
			if err != nil {
				t.Fatalf("NewAPNSClient returned error: %v", err)
			}
			if client == nil {
				t.Fatal("NewAPNSClient returned nil client")
			}
			if got := client.Environment(); got != PushEnvironmentProduction {
				t.Fatalf("environment = %q, want %q", got, PushEnvironmentProduction)
			}
		})
	}
}

func TestNewAPNSClientRejectsInvalidKeyValue(t *testing.T) {
	_, err := NewAPNSClient(&config.Env{
		APNSKeyID:     "KEYID12345",
		APNSTeamID:    "TEAMID1234",
		APNSBundleID:  "app.rxlab.podcast",
		APNSKeyBase64: "not a key",
	})
	if err == nil {
		t.Fatal("NewAPNSClient returned nil error")
	}
	if !strings.Contains(err.Error(), "decode APNS_KEY_BASE64") {
		t.Fatalf("error = %q, want APNS_KEY_BASE64 decode context", err.Error())
	}
}

func testAPNSPrivateKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}
