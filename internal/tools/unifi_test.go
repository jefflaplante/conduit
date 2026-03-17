package tools

import (
	"net/http"
	"os"
	"testing"
)

func TestUniFiTool_TLSDefaultSecure(t *testing.T) {
	// Verify that unifiHTTPClient returns a client with TLS verification enabled by default
	// (InsecureSkipVerify should be false)

	// Ensure the env var is NOT set
	os.Unsetenv("UNIFI_INSECURE_TLS")

	client := unifiHTTPClient()

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Expected *http.Transport as client transport")
	}

	if transport.TLSClientConfig == nil {
		// nil TLSClientConfig means default (secure) behavior, which is fine
		return
	}

	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("Default TLS configuration should NOT skip verification (InsecureSkipVerify must be false)")
	}
}

func TestUniFiTool_TLSInsecureWhenEnvSet(t *testing.T) {
	// Verify that setting UNIFI_INSECURE_TLS=true enables insecure mode
	os.Setenv("UNIFI_INSECURE_TLS", "true")
	defer os.Unsetenv("UNIFI_INSECURE_TLS")

	client := unifiHTTPClient()

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Expected *http.Transport as client transport")
	}

	if transport.TLSClientConfig == nil {
		t.Fatal("Expected TLSClientConfig to be set when UNIFI_INSECURE_TLS=true")
	}

	if !transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("TLS InsecureSkipVerify should be true when UNIFI_INSECURE_TLS=true")
	}
}

func TestUniFiTool_TLSSecureWhenEnvFalse(t *testing.T) {
	// Verify that setting UNIFI_INSECURE_TLS to anything other than "true" keeps secure mode
	os.Setenv("UNIFI_INSECURE_TLS", "false")
	defer os.Unsetenv("UNIFI_INSECURE_TLS")

	client := unifiHTTPClient()

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Expected *http.Transport as client transport")
	}

	if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("TLS InsecureSkipVerify should be false when UNIFI_INSECURE_TLS is not 'true'")
	}
}
