// Package main provides blank imports for optional tool registration.
// Each optional tool package has its own file with a build tag.
// This file serves as documentation only - actual imports are in separate files.
package main

// Optional tools are imported via build-tagged files:
// - optional_datadog.go (with_datadog)
// - optional_k8s.go (with_k8s)
// - optional_pagerduty.go (with_pagerduty)
// - optional_sre.go (with_sre)
// - optional_mqtt.go (with_mqtt)
// - optional_ssh.go (with_ssh)
// - optional_unifi.go (with_unifi)
//
// Build with tags to include tools:
//   go build -tags "with_datadog,with_k8s" ./cmd/gateway
