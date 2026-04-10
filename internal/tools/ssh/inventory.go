//go:build with_ssh

// Package ssh implements Ansible inventory parsing for SSH host management.
package ssh

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"conduit/internal/config"

	"gopkg.in/yaml.v3"
)

// InventorySource represents a source of inventory data
type InventorySource struct {
	Type       string // "file", "dynamic"
	Path       string // File path or script path
	LastLoaded time.Time
	Error      error
}

// InventoryManager manages SSH host inventories from multiple sources
type InventoryManager struct {
	mu              sync.RWMutex
	configHosts     []config.SSHHostConfig // Hosts from config (take precedence)
	inventoryHosts  []config.SSHHostConfig // Hosts from inventory sources
	sources         []InventorySource
	hostsByGroup    map[string][]string // Group name -> host names
	autoRefresh     bool
	refreshInterval time.Duration
	stopChan        chan struct{}
}

// NewInventoryManager creates a new inventory manager with config hosts
func NewInventoryManager(configHosts []config.SSHHostConfig) *InventoryManager {
	return &InventoryManager{
		configHosts:    configHosts,
		inventoryHosts: []config.SSHHostConfig{},
		sources:        []InventorySource{},
		hostsByGroup:   make(map[string][]string),
		stopChan:       make(chan struct{}),
	}
}

// LoadFile loads an inventory file (auto-detects INI or YAML format)
func (im *InventoryManager) LoadFile(path string) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	// Detect format based on extension
	ext := strings.ToLower(filepath.Ext(path))
	var hosts []config.SSHHostConfig
	var groups map[string][]string
	var err error

	switch ext {
	case ".yaml", ".yml":
		hosts, groups, err = im.parseYAMLInventory(path)
	case ".ini", "":
		// Default to INI if no extension or .ini
		hosts, groups, err = im.parseINIInventory(path)
	default:
		return fmt.Errorf("unsupported inventory file format: %s (supported: .ini, .yaml, .yml)", ext)
	}

	if err != nil {
		// Record the error in sources
		im.recordSource("file", path, err)
		return fmt.Errorf("failed to parse inventory file %s: %w", path, err)
	}

	// Add hosts and groups
	im.mergeInventoryHosts(hosts)
	im.mergeGroups(groups)

	// Record successful load
	im.recordSource("file", path, nil)

	return nil
}

// LoadDynamic executes a dynamic inventory script and parses the JSON output
func (im *InventoryManager) LoadDynamic(scriptPath string) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	// Check if script exists and is executable
	info, err := os.Stat(scriptPath)
	if err != nil {
		im.recordSource("dynamic", scriptPath, err)
		return fmt.Errorf("dynamic inventory script not found: %w", err)
	}

	if info.IsDir() {
		err := fmt.Errorf("path is a directory, not a script")
		im.recordSource("dynamic", scriptPath, err)
		return err
	}

	// Execute the script with --list flag
	cmd := exec.Command(scriptPath, "--list")
	output, err := cmd.Output()
	if err != nil {
		im.recordSource("dynamic", scriptPath, err)
		return fmt.Errorf("failed to execute dynamic inventory script: %w", err)
	}

	// Parse JSON output
	hosts, groups, err := im.parseDynamicInventory(output)
	if err != nil {
		im.recordSource("dynamic", scriptPath, err)
		return fmt.Errorf("failed to parse dynamic inventory output: %w", err)
	}

	// Add hosts and groups
	im.mergeInventoryHosts(hosts)
	im.mergeGroups(groups)

	// Record successful load
	im.recordSource("dynamic", scriptPath, nil)

	return nil
}

// GetHosts returns the merged list of hosts (config hosts take precedence)
func (im *InventoryManager) GetHosts() []config.SSHHostConfig {
	im.mu.RLock()
	defer im.mu.RUnlock()

	// Build a map of config hosts by name
	configHostMap := make(map[string]config.SSHHostConfig)
	for _, host := range im.configHosts {
		configHostMap[host.Name] = host
	}

	// Start with config hosts
	result := make([]config.SSHHostConfig, 0, len(im.configHosts)+len(im.inventoryHosts))
	result = append(result, im.configHosts...)

	// Add inventory hosts that don't conflict with config hosts
	for _, host := range im.inventoryHosts {
		if _, exists := configHostMap[host.Name]; !exists {
			result = append(result, host)
		}
	}

	return result
}

// GetHostsByGroup returns hosts belonging to a specific group
func (im *InventoryManager) GetHostsByGroup(group string) []config.SSHHostConfig {
	im.mu.RLock()
	defer im.mu.RUnlock()

	hostNames, ok := im.hostsByGroup[group]
	if !ok {
		return []config.SSHHostConfig{}
	}

	// Build a map of all hosts
	allHosts := im.GetHosts()
	hostMap := make(map[string]config.SSHHostConfig)
	for _, host := range allHosts {
		hostMap[host.Name] = host
	}

	// Collect hosts in the group
	result := make([]config.SSHHostConfig, 0, len(hostNames))
	for _, name := range hostNames {
		if host, exists := hostMap[name]; exists {
			result = append(result, host)
		}
	}

	return result
}

// GetGroups returns all group names
func (im *InventoryManager) GetGroups() []string {
	im.mu.RLock()
	defer im.mu.RUnlock()

	groups := make([]string, 0, len(im.hostsByGroup))
	for group := range im.hostsByGroup {
		groups = append(groups, group)
	}
	return groups
}

// Refresh reloads all inventory sources
func (im *InventoryManager) Refresh() error {
	im.mu.Lock()
	sources := make([]InventorySource, len(im.sources))
	copy(sources, im.sources)
	im.mu.Unlock()

	var errors []error
	for _, source := range sources {
		var err error
		switch source.Type {
		case "file":
			err = im.LoadFile(source.Path)
		case "dynamic":
			err = im.LoadDynamic(source.Path)
		}
		if err != nil {
			errors = append(errors, fmt.Errorf("%s %s: %w", source.Type, source.Path, err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("refresh errors: %v", errors)
	}

	return nil
}

// StartAutoRefresh starts automatic refresh of inventories
func (im *InventoryManager) StartAutoRefresh(interval time.Duration) {
	im.mu.Lock()
	im.autoRefresh = true
	im.refreshInterval = interval
	stopChan := im.stopChan // Capture the channel
	im.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := im.Refresh(); err != nil {
					// Log error but continue refreshing
					fmt.Fprintf(os.Stderr, "inventory auto-refresh error: %v\n", err)
				}
			case <-stopChan:
				return
			}
		}
	}()
}

// StopAutoRefresh stops automatic refresh
func (im *InventoryManager) StopAutoRefresh() {
	im.mu.Lock()
	wasRunning := im.autoRefresh
	if wasRunning {
		im.autoRefresh = false
	}
	stopChan := im.stopChan
	im.mu.Unlock()

	// Close the channel outside the lock to avoid race
	if wasRunning {
		close(stopChan)
	}

	// Recreate channel for potential restart
	im.mu.Lock()
	im.stopChan = make(chan struct{})
	im.mu.Unlock()
}

// GetSources returns information about loaded inventory sources
func (im *InventoryManager) GetSources() []InventorySource {
	im.mu.RLock()
	defer im.mu.RUnlock()

	result := make([]InventorySource, len(im.sources))
	copy(result, im.sources)
	return result
}

// parseINIInventory parses an Ansible INI format inventory file
func (im *InventoryManager) parseINIInventory(path string) ([]config.SSHHostConfig, map[string][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	var hosts []config.SSHHostConfig
	groups := make(map[string][]string)
	currentGroup := ""
	childGroups := make(map[string][]string) // For [group:children] sections

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// Check for group header
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentGroup = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}

		// Parse host entry or group children
		if currentGroup != "" {
			// Check if this is a [group:children] section
			if strings.HasSuffix(currentGroup, ":children") {
				parentGroup := strings.TrimSuffix(currentGroup, ":children")
				childGroups[parentGroup] = append(childGroups[parentGroup], line)
			} else if strings.HasSuffix(currentGroup, ":vars") {
				// Skip group variables sections for now
				continue
			} else {
				// Parse host entry
				host, err := im.parseINIHostLine(line, currentGroup)
				if err != nil {
					return nil, nil, fmt.Errorf("line %d: %w", lineNum, err)
				}
				hosts = append(hosts, host)
				groups[currentGroup] = append(groups[currentGroup], host.Name)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}

	// Expand child groups
	im.expandChildGroups(groups, childGroups)

	return hosts, groups, nil
}

// parseINIHostLine parses a single host line from INI inventory
func (im *InventoryManager) parseINIHostLine(line, group string) (config.SSHHostConfig, error) {
	// Format: hostname [ansible_key=value ...]
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return config.SSHHostConfig{}, fmt.Errorf("empty host line")
	}

	hostname := parts[0]
	host := config.SSHHostConfig{
		Name:     hostname,
		Hostname: hostname,
		Groups:   []string{group},
	}

	// Parse ansible variables
	for _, part := range parts[1:] {
		if !strings.Contains(part, "=") {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])

		switch key {
		case "ansible_host":
			host.Hostname = value
		case "ansible_user":
			host.User = value
		case "ansible_port":
			fmt.Sscanf(value, "%d", &host.Port)
		case "ansible_ssh_private_key_file":
			host.IdentityFile = value
		}
	}

	return host, nil
}

// parseYAMLInventory parses an Ansible YAML format inventory file
func (im *InventoryManager) parseYAMLInventory(path string) ([]config.SSHHostConfig, map[string][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	var inventory map[string]interface{}
	if err := yaml.Unmarshal(data, &inventory); err != nil {
		return nil, nil, err
	}

	var hosts []config.SSHHostConfig
	groups := make(map[string][]string)

	// Parse the inventory structure - iterate through all top-level groups
	for groupName, groupData := range inventory {
		im.parseYAMLGroup(groupData, groupName, &hosts, groups)
	}

	return hosts, groups, nil
}

// parseYAMLGroup recursively parses YAML inventory groups
func (im *InventoryManager) parseYAMLGroup(data interface{}, groupName string, hosts *[]config.SSHHostConfig, groups map[string][]string) {
	group, ok := data.(map[string]interface{})
	if !ok {
		return
	}

	// Parse hosts
	if hostsData, ok := group["hosts"].(map[string]interface{}); ok {
		for hostName, hostData := range hostsData {
			host := config.SSHHostConfig{
				Name:     hostName,
				Hostname: hostName,
				Groups:   []string{groupName},
			}

			// Parse host variables
			if hostVars, ok := hostData.(map[string]interface{}); ok {
				if ansibleHost, ok := hostVars["ansible_host"].(string); ok {
					host.Hostname = ansibleHost
				}
				if ansibleUser, ok := hostVars["ansible_user"].(string); ok {
					host.User = ansibleUser
				}
				if ansiblePort, ok := hostVars["ansible_port"].(int); ok {
					host.Port = ansiblePort
				}
				if ansibleKey, ok := hostVars["ansible_ssh_private_key_file"].(string); ok {
					host.IdentityFile = ansibleKey
				}
			}

			*hosts = append(*hosts, host)
			groups[groupName] = append(groups[groupName], hostName)
		}
	}

	// Parse child groups
	if childrenData, ok := group["children"].(map[string]interface{}); ok {
		for childName, childData := range childrenData {
			im.parseYAMLGroup(childData, childName, hosts, groups)
		}
	}
}

// parseDynamicInventory parses JSON output from a dynamic inventory script
func (im *InventoryManager) parseDynamicInventory(data []byte) ([]config.SSHHostConfig, map[string][]string, error) {
	var inventory map[string]interface{}
	if err := json.Unmarshal(data, &inventory); err != nil {
		return nil, nil, err
	}

	var hosts []config.SSHHostConfig
	groups := make(map[string][]string)
	hostVars := make(map[string]map[string]interface{})

	// Parse groups
	for groupName, groupData := range inventory {
		if groupName == "_meta" {
			// Parse host variables from _meta.hostvars
			if meta, ok := groupData.(map[string]interface{}); ok {
				if hostvars, ok := meta["hostvars"].(map[string]interface{}); ok {
					for hostName, vars := range hostvars {
						if varMap, ok := vars.(map[string]interface{}); ok {
							hostVars[hostName] = varMap
						}
					}
				}
			}
			continue
		}

		group, ok := groupData.(map[string]interface{})
		if !ok {
			continue
		}

		// Get hosts from group
		if hostsList, ok := group["hosts"].([]interface{}); ok {
			for _, hostItem := range hostsList {
				if hostName, ok := hostItem.(string); ok {
					groups[groupName] = append(groups[groupName], hostName)
				}
			}
		}
	}

	// Build host configs
	seenHosts := make(map[string]bool)
	for groupName, hostNames := range groups {
		for _, hostName := range hostNames {
			if seenHosts[hostName] {
				// Update groups for existing host
				for i, host := range hosts {
					if host.Name == hostName {
						hosts[i].Groups = append(hosts[i].Groups, groupName)
						break
					}
				}
				continue
			}

			host := config.SSHHostConfig{
				Name:     hostName,
				Hostname: hostName,
				Groups:   []string{groupName},
			}

			// Apply host variables
			if vars, ok := hostVars[hostName]; ok {
				if ansibleHost, ok := vars["ansible_host"].(string); ok {
					host.Hostname = ansibleHost
				}
				if ansibleUser, ok := vars["ansible_user"].(string); ok {
					host.User = ansibleUser
				}
				if ansiblePort, ok := vars["ansible_port"].(float64); ok {
					host.Port = int(ansiblePort)
				}
				if ansibleKey, ok := vars["ansible_ssh_private_key_file"].(string); ok {
					host.IdentityFile = ansibleKey
				}
			}

			hosts = append(hosts, host)
			seenHosts[hostName] = true
		}
	}

	return hosts, groups, nil
}

// mergeInventoryHosts merges new hosts into the inventory hosts list
func (im *InventoryManager) mergeInventoryHosts(newHosts []config.SSHHostConfig) {
	hostMap := make(map[string]config.SSHHostConfig)

	// Start with existing inventory hosts
	for _, host := range im.inventoryHosts {
		hostMap[host.Name] = host
	}

	// Merge new hosts
	for _, host := range newHosts {
		if existing, exists := hostMap[host.Name]; exists {
			// Merge groups
			groupSet := make(map[string]bool)
			for _, g := range existing.Groups {
				groupSet[g] = true
			}
			for _, g := range host.Groups {
				groupSet[g] = true
			}
			host.Groups = make([]string, 0, len(groupSet))
			for g := range groupSet {
				host.Groups = append(host.Groups, g)
			}
		}
		hostMap[host.Name] = host
	}

	// Rebuild inventory hosts list
	im.inventoryHosts = make([]config.SSHHostConfig, 0, len(hostMap))
	for _, host := range hostMap {
		im.inventoryHosts = append(im.inventoryHosts, host)
	}
}

// mergeGroups merges new group mappings
func (im *InventoryManager) mergeGroups(newGroups map[string][]string) {
	for group, hostNames := range newGroups {
		existing := im.hostsByGroup[group]
		hostSet := make(map[string]bool)

		// Add existing hosts
		for _, name := range existing {
			hostSet[name] = true
		}

		// Add new hosts
		for _, name := range hostNames {
			hostSet[name] = true
		}

		// Rebuild list
		merged := make([]string, 0, len(hostSet))
		for name := range hostSet {
			merged = append(merged, name)
		}
		im.hostsByGroup[group] = merged
	}
}

// expandChildGroups expands [group:children] definitions
func (im *InventoryManager) expandChildGroups(groups map[string][]string, childGroups map[string][]string) {
	for parentGroup, childNames := range childGroups {
		for _, childName := range childNames {
			if childHosts, ok := groups[childName]; ok {
				groups[parentGroup] = append(groups[parentGroup], childHosts...)
			}
		}
	}
}

// recordSource records or updates a source in the sources list
func (im *InventoryManager) recordSource(typ, path string, err error) {
	now := time.Now()
	found := false

	for i, source := range im.sources {
		if source.Type == typ && source.Path == path {
			im.sources[i].LastLoaded = now
			im.sources[i].Error = err
			found = true
			break
		}
	}

	if !found {
		im.sources = append(im.sources, InventorySource{
			Type:       typ,
			Path:       path,
			LastLoaded: now,
			Error:      err,
		})
	}
}
