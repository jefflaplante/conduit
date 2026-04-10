//go:build with_ssh

package ssh

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSHTool_InventoryLoad tests loading inventory files
func TestSSHTool_InventoryLoad(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test inventory file
	iniFile := filepath.Join(tmpDir, "inventory.ini")
	iniContent := `
[webservers]
web1.example.com ansible_user=deploy
web2.example.com ansible_user=deploy ansible_port=2222

[dbservers]
db1.example.com ansible_user=postgres
`
	err := os.WriteFile(iniFile, []byte(iniContent), 0644)
	require.NoError(t, err)

	cfg := &config.RemoteSSHConfig{
		Enabled: true,
		Hosts:   []config.SSHHostConfig{},
		Security: config.SSHSecurityConfig{
			DefaultTier: "dangerous",
		},
		Audit: config.SSHAuditConfig{
			Enabled: false,
		},
	}

	tool, err := NewSSHTool(nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, tool)
	defer tool.Close()

	// Test loading inventory
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "inventory_load",
		"path":   iniFile,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Data, "hosts_count")
	assert.Equal(t, 3, result.Data["hosts_count"])
}

// TestSSHTool_InventoryList tests listing inventory hosts
func TestSSHTool_InventoryList(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test inventory file
	iniFile := filepath.Join(tmpDir, "inventory.ini")
	iniContent := `
[webservers]
web1.example.com
web2.example.com

[dbservers]
db1.example.com
`
	err := os.WriteFile(iniFile, []byte(iniContent), 0644)
	require.NoError(t, err)

	cfg := &config.RemoteSSHConfig{
		Enabled: true,
		Hosts:   []config.SSHHostConfig{},
		Security: config.SSHSecurityConfig{
			DefaultTier: "dangerous",
		},
		Audit: config.SSHAuditConfig{
			Enabled: false,
		},
	}

	tool, err := NewSSHTool(nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, tool)
	defer tool.Close()

	// Load inventory first
	_, err = tool.Execute(context.Background(), map[string]interface{}{
		"action": "inventory_load",
		"path":   iniFile,
	})
	require.NoError(t, err)

	// List all hosts
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "inventory_list",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Data, "hosts")
	assert.Contains(t, result.Data, "count")
	assert.Equal(t, 3, result.Data["count"])

	// List hosts by group
	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"action": "inventory_list",
		"group":  "webservers",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, 2, result.Data["count"])
}

// TestSSHTool_InventoryRefresh tests refreshing inventory
func TestSSHTool_InventoryRefresh(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial inventory
	iniFile := filepath.Join(tmpDir, "inventory.ini")
	iniContent1 := `
[webservers]
web1.example.com
`
	err := os.WriteFile(iniFile, []byte(iniContent1), 0644)
	require.NoError(t, err)

	cfg := &config.RemoteSSHConfig{
		Enabled: true,
		Hosts:   []config.SSHHostConfig{},
		Security: config.SSHSecurityConfig{
			DefaultTier: "dangerous",
		},
		Audit: config.SSHAuditConfig{
			Enabled: false,
		},
	}

	tool, err := NewSSHTool(nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, tool)
	defer tool.Close()

	// Load inventory
	_, err = tool.Execute(context.Background(), map[string]interface{}{
		"action": "inventory_load",
		"path":   iniFile,
	})
	require.NoError(t, err)

	// Update inventory file
	iniContent2 := `
[webservers]
web1.example.com
web2.example.com
web3.example.com
`
	err = os.WriteFile(iniFile, []byte(iniContent2), 0644)
	require.NoError(t, err)

	// Refresh inventory
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "inventory_refresh",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Data, "hosts_count")
	assert.Equal(t, 3, result.Data["hosts_count"])
}

// TestSSHTool_InventoryLoadDynamic tests loading dynamic inventory
func TestSSHTool_InventoryLoadDynamic(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a dynamic inventory script
	scriptFile := filepath.Join(tmpDir, "inventory.sh")
	scriptContent := `#!/bin/bash
cat <<'JSONEOF'
{
  "webservers": {
    "hosts": ["web1.example.com", "web2.example.com"]
  },
  "_meta": {
    "hostvars": {
      "web1.example.com": {
        "ansible_user": "deploy"
      },
      "web2.example.com": {
        "ansible_user": "deploy"
      }
    }
  }
}
JSONEOF
`
	err := os.WriteFile(scriptFile, []byte(scriptContent), 0755)
	require.NoError(t, err)

	cfg := &config.RemoteSSHConfig{
		Enabled: true,
		Hosts:   []config.SSHHostConfig{},
		Security: config.SSHSecurityConfig{
			DefaultTier: "dangerous",
		},
		Audit: config.SSHAuditConfig{
			Enabled: false,
		},
	}

	tool, err := NewSSHTool(nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, tool)
	defer tool.Close()

	// Test loading dynamic inventory
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "inventory_load",
		"path":   scriptFile,
		"type":   "dynamic",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Data, "hosts_count")
	assert.Equal(t, 2, result.Data["hosts_count"])
}

// TestSSHTool_InventoryConfigPrecedence tests that config hosts take precedence
func TestSSHTool_InventoryConfigPrecedence(t *testing.T) {
	tmpDir := t.TempDir()

	// Create inventory file with web1
	iniFile := filepath.Join(tmpDir, "inventory.ini")
	iniContent := `
[webservers]
web1.example.com ansible_user=inventory_user ansible_port=2222
web2.example.com ansible_user=deploy
`
	err := os.WriteFile(iniFile, []byte(iniContent), 0644)
	require.NoError(t, err)

	// Config has web1 with different settings
	cfg := &config.RemoteSSHConfig{
		Enabled: true,
		Hosts: []config.SSHHostConfig{
			{
				Name:     "web1.example.com",
				Hostname: "192.168.1.100",
				User:     "config_user",
				Port:     22,
			},
		},
		Security: config.SSHSecurityConfig{
			DefaultTier: "dangerous",
		},
		Audit: config.SSHAuditConfig{
			Enabled: false,
		},
	}

	tool, err := NewSSHTool(nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, tool)
	defer tool.Close()

	// Load inventory
	_, err = tool.Execute(context.Background(), map[string]interface{}{
		"action": "inventory_load",
		"path":   iniFile,
	})
	require.NoError(t, err)

	// List hosts
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "inventory_list",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify that web1 uses config settings, not inventory settings
	hosts := result.Data["hosts"].([]map[string]interface{})
	var web1 map[string]interface{}
	for _, host := range hosts {
		if host["name"] == "web1.example.com" {
			web1 = host
			break
		}
	}

	require.NotNil(t, web1)
	assert.Equal(t, "192.168.1.100", web1["hostname"])
	assert.Equal(t, "config_user", web1["user"])
	assert.Equal(t, 22, web1["port"])
}
