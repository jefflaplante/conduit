package mqtt

import (
	"encoding/json"
	"path"
	"strings"
	"sync"
	"time"
)

// Device represents a parsed zigbee2mqtt device from bridge/devices.
type Device struct {
	IEEEAddress  string `json:"ieee_address"`
	FriendlyName string `json:"friendly_name"`
	Type         string `json:"type"`
	ModelID      string `json:"model_id"`
	Manufacturer string `json:"manufacturer"`
	Description  string `json:"description"`
	Supported    bool   `json:"supported"`
	Disabled     bool   `json:"disabled"`
}

// DeviceRegistry maintains a parsed list of zigbee2mqtt devices.
type DeviceRegistry struct {
	mu          sync.RWMutex
	devices     []Device
	lastUpdated time.Time
}

// NewDeviceRegistry creates an empty device registry.
func NewDeviceRegistry() *DeviceRegistry {
	return &DeviceRegistry{}
}

// bridgeDevice mirrors the JSON structure of zigbee2mqtt/bridge/devices entries.
type bridgeDevice struct {
	IEEEAddress  string `json:"ieee_address"`
	FriendlyName string `json:"friendly_name"`
	Type         string `json:"type"`
	ModelID      string `json:"model_id"`
	Manufacturer string `json:"manufacturer"`
	Disabled     bool   `json:"disabled"`
	Supported    *bool  `json:"supported"`
	Definition   *struct {
		Description string `json:"description"`
	} `json:"definition"`
}

// Update parses a zigbee2mqtt/bridge/devices payload and replaces the device list.
func (dr *DeviceRegistry) Update(payload []byte) error {
	var raw []bridgeDevice
	if err := json.Unmarshal(payload, &raw); err != nil {
		return err
	}

	devices := make([]Device, 0, len(raw))
	for _, bd := range raw {
		d := Device{
			IEEEAddress:  bd.IEEEAddress,
			FriendlyName: bd.FriendlyName,
			Type:         bd.Type,
			ModelID:      bd.ModelID,
			Manufacturer: bd.Manufacturer,
			Disabled:     bd.Disabled,
			Supported:    bd.Supported != nil && *bd.Supported,
		}
		if bd.Definition != nil {
			d.Description = bd.Definition.Description
		}
		devices = append(devices, d)
	}

	dr.mu.Lock()
	dr.devices = devices
	dr.lastUpdated = time.Now()
	dr.mu.Unlock()

	debugf("[MQTT] Device registry updated: %d devices", len(devices))
	return nil
}

// Devices returns a copy of all parsed devices.
func (dr *DeviceRegistry) Devices() []Device {
	dr.mu.RLock()
	defer dr.mu.RUnlock()

	result := make([]Device, len(dr.devices))
	copy(result, dr.devices)
	return result
}

// Count returns the number of devices.
func (dr *DeviceRegistry) Count() int {
	dr.mu.RLock()
	defer dr.mu.RUnlock()
	return len(dr.devices)
}

// FindByName returns devices whose friendly name matches a glob pattern.
func (dr *DeviceRegistry) FindByName(pattern string) []Device {
	dr.mu.RLock()
	defer dr.mu.RUnlock()

	pattern = strings.ToLower(pattern)
	var result []Device
	for _, d := range dr.devices {
		if matched, _ := path.Match(pattern, strings.ToLower(d.FriendlyName)); matched {
			result = append(result, d)
		}
	}
	return result
}
