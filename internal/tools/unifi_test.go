package tools

import (
	"context"
	"net/http"
	"os"
	"testing"

	"conduit/internal/tools/types"
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

func TestUniFiTool_SelfTest_NoCredentials(t *testing.T) {
	// Ensure credentials are NOT set
	os.Unsetenv("UNVR_URL")
	os.Unsetenv("UNVR_API_KEY")

	tool := &UniFiTool{}
	result := tool.SelfTest(context.Background(), nil)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.Status != types.SelfTestStatusFailed {
		t.Errorf("Expected failed status without credentials, got %s", result.Status)
	}

	if result.IsFunctional() {
		t.Error("Tool should not be functional without credentials")
	}

	// Should have suggestions
	if len(result.Suggestions) == 0 {
		t.Error("Expected suggestions for missing credentials")
	}

	// Should have dependencies
	if len(result.Dependencies) == 0 {
		t.Error("Expected dependency information")
	}

	// Check UNVR_URL dependency
	foundURLDep := false
	for _, dep := range result.Dependencies {
		if dep.Name == "UNVR_URL" {
			foundURLDep = true
			if dep.Available {
				t.Error("UNVR_URL should not be available")
			}
		}
	}
	if !foundURLDep {
		t.Error("Expected UNVR_URL dependency")
	}
}

func TestUniFiTool_SelfTest_WithCredentials(t *testing.T) {
	// Set credentials (won't actually connect)
	os.Setenv("UNVR_URL", "https://192.168.1.1")
	os.Setenv("UNVR_API_KEY", "test-api-key")
	defer func() {
		os.Unsetenv("UNVR_URL")
		os.Unsetenv("UNVR_API_KEY")
	}()

	tool := &UniFiTool{}

	// Test without dependency check (won't try to connect)
	opts := &types.SelfTestOptions{
		CheckDependencies: false,
		IncludeExamples:   true,
	}
	result := tool.SelfTest(context.Background(), opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// With credentials set but no connectivity check, should be OK
	if result.Status != types.SelfTestStatusOK {
		t.Errorf("Expected OK status with credentials, got %s", result.Status)
	}

	// Should have capabilities
	if len(result.Capabilities) == 0 {
		t.Error("Expected capabilities")
	}

	// Should have examples
	if len(result.Examples) == 0 {
		t.Error("Expected examples when IncludeExamples is true")
	}
}

func TestUniFiTool_SelfTest_VerboseMode(t *testing.T) {
	os.Setenv("UNVR_URL", "https://192.168.1.1")
	os.Setenv("UNVR_API_KEY", "test-api-key")
	defer func() {
		os.Unsetenv("UNVR_URL")
		os.Unsetenv("UNVR_API_KEY")
	}()

	tool := &UniFiTool{}
	opts := &types.SelfTestOptions{
		Verbose:           true,
		CheckDependencies: false,
	}
	result := tool.SelfTest(context.Background(), opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// In verbose mode, API key dependency should have char count message
	for _, dep := range result.Dependencies {
		if dep.Name == "UNVR_API_KEY" && dep.Available {
			if dep.Message == "" {
				t.Error("Expected message in verbose mode for API key")
			}
		}
	}
}

func TestUniFiTool_SelfTest_TLSStatus(t *testing.T) {
	os.Setenv("UNVR_URL", "https://192.168.1.1")
	os.Setenv("UNVR_API_KEY", "test-api-key")
	os.Setenv("UNIFI_INSECURE_TLS", "true")
	defer func() {
		os.Unsetenv("UNVR_URL")
		os.Unsetenv("UNVR_API_KEY")
		os.Unsetenv("UNIFI_INSECURE_TLS")
	}()

	tool := &UniFiTool{}
	opts := &types.SelfTestOptions{CheckDependencies: false}
	result := tool.SelfTest(context.Background(), opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Find TLS dependency
	for _, dep := range result.Dependencies {
		if dep.Name == "TLS" {
			if dep.Status != "insecure" {
				t.Errorf("Expected TLS status 'insecure', got '%s'", dep.Status)
			}
			return
		}
	}
	t.Error("Expected TLS dependency")
}
