package mqtt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleBridgeDevices = `[
  {
    "ieee_address": "0x00124b001cda7e90",
    "friendly_name": "Coordinator",
    "type": "Coordinator",
    "supported": false,
    "disabled": false,
    "definition": null
  },
  {
    "ieee_address": "0x842e14fffe3a5c21",
    "friendly_name": "Living Room Sensor",
    "type": "EndDevice",
    "model_id": "SNZB-02",
    "manufacturer": "SONOFF",
    "supported": true,
    "disabled": false,
    "definition": {
      "description": "Temperature and humidity sensor",
      "model": "SNZB-02"
    }
  },
  {
    "ieee_address": "0x680ae2fffe123456",
    "friendly_name": "Kitchen Light",
    "type": "Router",
    "model_id": "9290012573A",
    "manufacturer": "Philips",
    "supported": true,
    "disabled": false,
    "definition": {
      "description": "Hue white and color ambiance E26/E27",
      "model": "9290012573A"
    }
  }
]`

func TestDeviceRegistry_Update(t *testing.T) {
	dr := NewDeviceRegistry()
	err := dr.Update([]byte(sampleBridgeDevices))
	require.NoError(t, err)

	devices := dr.Devices()
	assert.Len(t, devices, 3)

	// Coordinator
	assert.Equal(t, "Coordinator", devices[0].FriendlyName)
	assert.Equal(t, "Coordinator", devices[0].Type)
	assert.False(t, devices[0].Supported)
	assert.Empty(t, devices[0].Description)

	// Sensor
	assert.Equal(t, "Living Room Sensor", devices[1].FriendlyName)
	assert.Equal(t, "EndDevice", devices[1].Type)
	assert.True(t, devices[1].Supported)
	assert.Equal(t, "SONOFF", devices[1].Manufacturer)
	assert.Equal(t, "Temperature and humidity sensor", devices[1].Description)

	// Light
	assert.Equal(t, "Kitchen Light", devices[2].FriendlyName)
	assert.Equal(t, "Router", devices[2].Type)
	assert.Equal(t, "Philips", devices[2].Manufacturer)
}

func TestDeviceRegistry_Count(t *testing.T) {
	dr := NewDeviceRegistry()
	assert.Equal(t, 0, dr.Count())

	err := dr.Update([]byte(sampleBridgeDevices))
	require.NoError(t, err)
	assert.Equal(t, 3, dr.Count())
}

func TestDeviceRegistry_EmptyArray(t *testing.T) {
	dr := NewDeviceRegistry()
	err := dr.Update([]byte(`[]`))
	require.NoError(t, err)
	assert.Equal(t, 0, dr.Count())
}

func TestDeviceRegistry_InvalidJSON(t *testing.T) {
	dr := NewDeviceRegistry()
	err := dr.Update([]byte(`not json`))
	assert.Error(t, err)
	assert.Equal(t, 0, dr.Count())
}

func TestDeviceRegistry_PartialFields(t *testing.T) {
	dr := NewDeviceRegistry()
	err := dr.Update([]byte(`[{"ieee_address":"0xabc","friendly_name":"Minimal"}]`))
	require.NoError(t, err)

	devices := dr.Devices()
	require.Len(t, devices, 1)
	assert.Equal(t, "Minimal", devices[0].FriendlyName)
	assert.Equal(t, "0xabc", devices[0].IEEEAddress)
	assert.False(t, devices[0].Supported) // nil pointer → false
	assert.Empty(t, devices[0].Description)
}

func TestDeviceRegistry_FindByName(t *testing.T) {
	dr := NewDeviceRegistry()
	err := dr.Update([]byte(sampleBridgeDevices))
	require.NoError(t, err)

	// Exact match (case-insensitive)
	found := dr.FindByName("kitchen light")
	assert.Len(t, found, 1)
	assert.Equal(t, "Kitchen Light", found[0].FriendlyName)

	// Glob match
	found = dr.FindByName("*sensor*")
	assert.Len(t, found, 1)
	assert.Equal(t, "Living Room Sensor", found[0].FriendlyName)

	// Glob with wildcard prefix
	found = dr.FindByName("*light")
	assert.Len(t, found, 1)
	assert.Equal(t, "Kitchen Light", found[0].FriendlyName)

	// No match
	found = dr.FindByName("nonexistent")
	assert.Len(t, found, 0)
}

func TestDeviceRegistry_UpdateReplacesDevices(t *testing.T) {
	dr := NewDeviceRegistry()

	err := dr.Update([]byte(sampleBridgeDevices))
	require.NoError(t, err)
	assert.Equal(t, 3, dr.Count())

	// Update with fewer devices replaces the old list
	err = dr.Update([]byte(`[{"ieee_address":"0x1","friendly_name":"Only One"}]`))
	require.NoError(t, err)
	assert.Equal(t, 1, dr.Count())
	assert.Equal(t, "Only One", dr.Devices()[0].FriendlyName)
}
