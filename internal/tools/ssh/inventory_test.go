package ssh

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewInventoryManager(t *testing.T) {
	configHosts := []config.SSHHostConfig{
		{Name: "host1", Hostname: "192.168.1.1"},
		{Name: "host2", Hostname: "192.168.1.2"},
	}

	im := NewInventoryManager(configHosts)
	assert.NotNil(t, im)
	assert.Len(t, im.configHosts, 2)
	assert.Len(t, im.inventoryHosts, 0)
}

func TestLoadINIInventory(t *testing.T) {
	// Create a temporary INI inventory file
	tmpDir := t.TempDir()
	iniFile := filepath.Join(tmpDir, "inventory.ini")

	iniContent := `
# Web servers
[webservers]
web1.example.com ansible_user=deploy
web2.example.com ansible_user=deploy ansible_port=2222

[dbservers]
db1.example.com ansible_host=10.0.0.1 ansible_user=postgres
db2.example.com ansible_host=10.0.0.2 ansible_user=postgres

[production:children]
webservers
dbservers
`

	err := os.WriteFile(iniFile, []byte(iniContent), 0644)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})
	err = im.LoadFile(iniFile)
	require.NoError(t, err)

	// Check hosts
	hosts := im.GetHosts()
	assert.Len(t, hosts, 4)

	// Check groups
	groups := im.GetGroups()
	assert.Contains(t, groups, "webservers")
	assert.Contains(t, groups, "dbservers")

	// Check webservers group
	webHosts := im.GetHostsByGroup("webservers")
	assert.Len(t, webHosts, 2)

	// Verify host details
	for _, host := range webHosts {
		assert.Equal(t, "deploy", host.User)
		if host.Name == "web2.example.com" {
			assert.Equal(t, 2222, host.Port)
		}
	}

	// Check dbservers group
	dbHosts := im.GetHostsByGroup("dbservers")
	assert.Len(t, dbHosts, 2)
	for _, host := range dbHosts {
		assert.Equal(t, "postgres", host.User)
		assert.Contains(t, host.Hostname, "10.0.0.")
	}
}

func TestLoadYAMLInventory(t *testing.T) {
	tmpDir := t.TempDir()
	yamlFile := filepath.Join(tmpDir, "inventory.yaml")

	yamlContent := `
all:
  children:
    webservers:
      hosts:
        web1.example.com:
          ansible_user: deploy
          ansible_port: 22
        web2.example.com:
          ansible_user: deploy
          ansible_port: 2222
    dbservers:
      hosts:
        db1.example.com:
          ansible_host: 10.0.0.1
          ansible_user: postgres
        db2.example.com:
          ansible_host: 10.0.0.2
          ansible_user: postgres
`

	err := os.WriteFile(yamlFile, []byte(yamlContent), 0644)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})
	err = im.LoadFile(yamlFile)
	require.NoError(t, err)

	// Check hosts
	hosts := im.GetHosts()
	assert.GreaterOrEqual(t, len(hosts), 4)

	// Check groups
	webHosts := im.GetHostsByGroup("webservers")
	assert.Len(t, webHosts, 2)

	// Verify host details
	var web2Found bool
	for _, host := range webHosts {
		assert.Equal(t, "deploy", host.User)
		if host.Name == "web2.example.com" {
			assert.Equal(t, 2222, host.Port)
			web2Found = true
		}
	}
	assert.True(t, web2Found, "web2.example.com should be in webservers group")

	// Check dbservers
	dbHosts := im.GetHostsByGroup("dbservers")
	assert.Len(t, dbHosts, 2)
	for _, host := range dbHosts {
		assert.Equal(t, "postgres", host.User)
	}
}

func TestLoadDynamicInventory(t *testing.T) {
	tmpDir := t.TempDir()
	scriptFile := filepath.Join(tmpDir, "inventory.sh")

	// Create a simple dynamic inventory script
	scriptContent := `#!/bin/bash
cat <<EOF
{
  "webservers": {
    "hosts": ["web1.example.com", "web2.example.com"]
  },
  "dbservers": {
    "hosts": ["db1.example.com", "db2.example.com"]
  },
  "_meta": {
    "hostvars": {
      "web1.example.com": {
        "ansible_user": "deploy",
        "ansible_port": 22
      },
      "web2.example.com": {
        "ansible_user": "deploy",
        "ansible_port": 2222
      },
      "db1.example.com": {
        "ansible_host": "10.0.0.1",
        "ansible_user": "postgres"
      },
      "db2.example.com": {
        "ansible_host": "10.0.0.2",
        "ansible_user": "postgres"
      }
    }
  }
}
EOF
`

	err := os.WriteFile(scriptFile, []byte(scriptContent), 0755)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})
	err = im.LoadDynamic(scriptFile)
	require.NoError(t, err)

	// Check hosts
	hosts := im.GetHosts()
	assert.Len(t, hosts, 4)

	// Check groups
	webHosts := im.GetHostsByGroup("webservers")
	assert.Len(t, webHosts, 2)

	// Verify host details
	for _, host := range webHosts {
		assert.Equal(t, "deploy", host.User)
	}

	dbHosts := im.GetHostsByGroup("dbservers")
	assert.Len(t, dbHosts, 2)
	for _, host := range dbHosts {
		assert.Equal(t, "postgres", host.User)
	}
}

func TestConfigHostsPrecedence(t *testing.T) {
	// Config hosts should take precedence over inventory hosts
	configHosts := []config.SSHHostConfig{
		{
			Name:     "web1.example.com",
			Hostname: "192.168.1.100",
			User:     "admin",
			Port:     22,
		},
	}

	im := NewInventoryManager(configHosts)

	// Load inventory with conflicting host
	tmpDir := t.TempDir()
	iniFile := filepath.Join(tmpDir, "inventory.ini")
	iniContent := `
[webservers]
web1.example.com ansible_user=deploy ansible_port=2222
web2.example.com ansible_user=deploy
`
	err := os.WriteFile(iniFile, []byte(iniContent), 0644)
	require.NoError(t, err)

	err = im.LoadFile(iniFile)
	require.NoError(t, err)

	// Get merged hosts
	hosts := im.GetHosts()
	assert.Len(t, hosts, 2) // web1 (config) + web2 (inventory)

	// Find web1 - should have config values, not inventory values
	var web1 *config.SSHHostConfig
	for _, host := range hosts {
		if host.Name == "web1.example.com" {
			web1Copy := host
			web1 = &web1Copy
			break
		}
	}

	require.NotNil(t, web1, "web1.example.com should exist")
	assert.Equal(t, "192.168.1.100", web1.Hostname, "Should use config hostname")
	assert.Equal(t, "admin", web1.User, "Should use config user")
	assert.Equal(t, 22, web1.Port, "Should use config port")

	// web2 should have inventory values
	var web2 *config.SSHHostConfig
	for _, host := range hosts {
		if host.Name == "web2.example.com" {
			web2Copy := host
			web2 = &web2Copy
			break
		}
	}

	require.NotNil(t, web2, "web2.example.com should exist")
	assert.Equal(t, "deploy", web2.User, "Should use inventory user")
}

func TestGroupExpansion(t *testing.T) {
	// Test [group:children] expansion
	tmpDir := t.TempDir()
	iniFile := filepath.Join(tmpDir, "inventory.ini")
	iniContent := `
[web-prod]
web-prod-1.example.com
web-prod-2.example.com

[web-staging]
web-staging-1.example.com

[db-prod]
db-prod-1.example.com

[production:children]
web-prod
db-prod

[webservers:children]
web-prod
web-staging
`
	err := os.WriteFile(iniFile, []byte(iniContent), 0644)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})
	err = im.LoadFile(iniFile)
	require.NoError(t, err)

	// Check production group (should contain web-prod and db-prod hosts)
	prodHosts := im.GetHostsByGroup("production")
	assert.GreaterOrEqual(t, len(prodHosts), 3, "production group should have at least 3 hosts")

	// Check webservers group (should contain web-prod and web-staging hosts)
	webHosts := im.GetHostsByGroup("webservers")
	assert.GreaterOrEqual(t, len(webHosts), 3, "webservers group should have at least 3 hosts")
}

func TestRefresh(t *testing.T) {
	tmpDir := t.TempDir()
	iniFile := filepath.Join(tmpDir, "inventory.ini")

	// Write initial inventory
	iniContent1 := `
[webservers]
web1.example.com
`
	err := os.WriteFile(iniFile, []byte(iniContent1), 0644)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})
	err = im.LoadFile(iniFile)
	require.NoError(t, err)

	hosts := im.GetHosts()
	assert.Len(t, hosts, 1)

	// Update inventory file
	iniContent2 := `
[webservers]
web1.example.com
web2.example.com
web3.example.com
`
	err = os.WriteFile(iniFile, []byte(iniContent2), 0644)
	require.NoError(t, err)

	// Refresh
	err = im.Refresh()
	require.NoError(t, err)

	hosts = im.GetHosts()
	assert.Len(t, hosts, 3)
}

func TestAutoRefresh(t *testing.T) {
	tmpDir := t.TempDir()
	iniFile := filepath.Join(tmpDir, "inventory.ini")

	// Write initial inventory
	iniContent1 := `
[webservers]
web1.example.com
`
	err := os.WriteFile(iniFile, []byte(iniContent1), 0644)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})
	err = im.LoadFile(iniFile)
	require.NoError(t, err)

	// Start auto-refresh with a short interval
	im.StartAutoRefresh(100 * time.Millisecond)
	defer im.StopAutoRefresh()

	hosts := im.GetHosts()
	assert.Len(t, hosts, 1)

	// Update inventory file
	iniContent2 := `
[webservers]
web1.example.com
web2.example.com
`
	err = os.WriteFile(iniFile, []byte(iniContent2), 0644)
	require.NoError(t, err)

	// Wait for auto-refresh
	time.Sleep(250 * time.Millisecond)

	hosts = im.GetHosts()
	assert.Len(t, hosts, 2)
}

func TestGetSources(t *testing.T) {
	tmpDir := t.TempDir()
	iniFile := filepath.Join(tmpDir, "inventory.ini")
	iniContent := `
[webservers]
web1.example.com
`
	err := os.WriteFile(iniFile, []byte(iniContent), 0644)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})
	err = im.LoadFile(iniFile)
	require.NoError(t, err)

	sources := im.GetSources()
	assert.Len(t, sources, 1)
	assert.Equal(t, "file", sources[0].Type)
	assert.Equal(t, iniFile, sources[0].Path)
	assert.Nil(t, sources[0].Error)
	assert.False(t, sources[0].LastLoaded.IsZero())
}

func TestMultipleSources(t *testing.T) {
	tmpDir := t.TempDir()

	// Create first inventory
	iniFile1 := filepath.Join(tmpDir, "inventory1.ini")
	iniContent1 := `
[webservers]
web1.example.com
`
	err := os.WriteFile(iniFile1, []byte(iniContent1), 0644)
	require.NoError(t, err)

	// Create second inventory
	iniFile2 := filepath.Join(tmpDir, "inventory2.ini")
	iniContent2 := `
[dbservers]
db1.example.com
`
	err = os.WriteFile(iniFile2, []byte(iniContent2), 0644)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})

	err = im.LoadFile(iniFile1)
	require.NoError(t, err)

	err = im.LoadFile(iniFile2)
	require.NoError(t, err)

	hosts := im.GetHosts()
	assert.Len(t, hosts, 2)

	groups := im.GetGroups()
	assert.Contains(t, groups, "webservers")
	assert.Contains(t, groups, "dbservers")

	sources := im.GetSources()
	assert.Len(t, sources, 2)
}

func TestInvalidInventoryFile(t *testing.T) {
	tmpDir := t.TempDir()
	invalidFile := filepath.Join(tmpDir, "invalid.yaml")

	// Create an invalid YAML file
	invalidContent := `
this is not: valid yaml: content
  - with: broken
    syntax: [unclosed bracket
`
	err := os.WriteFile(invalidFile, []byte(invalidContent), 0644)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})
	err = im.LoadFile(invalidFile)
	assert.Error(t, err)

	sources := im.GetSources()
	assert.Len(t, sources, 1)
	assert.NotNil(t, sources[0].Error)
}

func TestEmptyInventory(t *testing.T) {
	tmpDir := t.TempDir()
	emptyFile := filepath.Join(tmpDir, "empty.ini")

	err := os.WriteFile(emptyFile, []byte(""), 0644)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})
	err = im.LoadFile(emptyFile)
	require.NoError(t, err)

	hosts := im.GetHosts()
	assert.Len(t, hosts, 0)
}

func TestMergeHostGroups(t *testing.T) {
	tmpDir := t.TempDir()

	// First inventory assigns web1 to webservers group
	iniFile1 := filepath.Join(tmpDir, "inventory1.ini")
	iniContent1 := `
[webservers]
web1.example.com
`
	err := os.WriteFile(iniFile1, []byte(iniContent1), 0644)
	require.NoError(t, err)

	// Second inventory assigns web1 to production group
	iniFile2 := filepath.Join(tmpDir, "inventory2.ini")
	iniContent2 := `
[production]
web1.example.com
`
	err = os.WriteFile(iniFile2, []byte(iniContent2), 0644)
	require.NoError(t, err)

	im := NewInventoryManager([]config.SSHHostConfig{})

	err = im.LoadFile(iniFile1)
	require.NoError(t, err)

	err = im.LoadFile(iniFile2)
	require.NoError(t, err)

	// web1 should be in both groups
	webHosts := im.GetHostsByGroup("webservers")
	assert.Len(t, webHosts, 1)

	prodHosts := im.GetHostsByGroup("production")
	assert.Len(t, prodHosts, 1)

	// Should be the same host
	assert.Equal(t, webHosts[0].Name, prodHosts[0].Name)

	// Check that the host has both groups
	hosts := im.GetHosts()
	var web1 *config.SSHHostConfig
	for _, host := range hosts {
		if host.Name == "web1.example.com" {
			web1Copy := host
			web1 = &web1Copy
			break
		}
	}
	require.NotNil(t, web1)
	assert.Contains(t, web1.Groups, "webservers")
	assert.Contains(t, web1.Groups, "production")
}
