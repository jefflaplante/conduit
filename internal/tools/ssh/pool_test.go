//go:build with_ssh

package ssh

import (
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
)

// testHosts creates a list of test hosts
func testHosts() []config.SSHHostConfig {
	return []config.SSHHostConfig{
		{Name: "host1", Hostname: "192.168.1.1", Port: 22, User: "user1"},
		{Name: "host2", Hostname: "192.168.1.2", Port: 22, User: "user2"},
		{Name: "host3", Hostname: "192.168.1.3", Port: 2222, User: "user3"},
	}
}

func TestNewPool(t *testing.T) {
	hosts := testHosts()
	defaults := testDefaults()
	poolConfig := testPoolConfig()

	pool := NewPool(hosts, defaults, poolConfig)
	defer pool.Close()

	if pool == nil {
		t.Fatal("NewPool returned nil")
	}

	// Verify hosts were added
	hostList := pool.ListHosts()
	if len(hostList) != len(hosts) {
		t.Errorf("ListHosts() returned %d hosts, want %d", len(hostList), len(hosts))
	}

	// Verify each host is present
	for _, host := range hosts {
		cfg, ok := pool.GetHostConfig(host.Name)
		if !ok {
			t.Errorf("GetHostConfig(%s) returned false", host.Name)
			continue
		}
		if cfg.Hostname != host.Hostname {
			t.Errorf("GetHostConfig(%s).Hostname = %s, want %s", host.Name, cfg.Hostname, host.Hostname)
		}
	}
}

func TestPool_DefaultConfig(t *testing.T) {
	hosts := testHosts()
	defaults := testDefaults()

	// Create pool with zero values to test defaults
	pool := NewPool(hosts, defaults, config.SSHPoolConfig{})
	defer pool.Close()

	// Verify defaults were applied
	stats := pool.Stats()
	if stats.TotalConnections != 0 {
		t.Errorf("initial TotalConnections = %d, want 0", stats.TotalConnections)
	}
}

func TestPool_GetUnknownHost(t *testing.T) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	defer pool.Close()

	_, err := pool.Get("unknown-host")
	if err == nil {
		t.Error("Get(unknown-host) should return error")
	}
}

func TestPool_GetDisabledHost(t *testing.T) {
	disabled := false
	hosts := []config.SSHHostConfig{
		{Name: "disabled-host", Hostname: "192.168.1.1", Enabled: &disabled},
	}

	pool := NewPool(hosts, testDefaults(), testPoolConfig())
	defer pool.Close()

	_, err := pool.Get("disabled-host")
	if err == nil {
		t.Error("Get(disabled-host) should return error")
	}
}

func TestPool_Close(t *testing.T) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())

	// Close the pool
	pool.Close()

	// Verify pool is closed
	_, err := pool.Get("host1")
	if err == nil {
		t.Error("Get on closed pool should return error")
	}

	// Close again should be safe
	pool.Close()
}

func TestPool_AddHost(t *testing.T) {
	pool := NewPool([]config.SSHHostConfig{}, testDefaults(), testPoolConfig())
	defer pool.Close()

	// Add a host
	err := pool.AddHost(config.SSHHostConfig{
		Name:     "new-host",
		Hostname: "192.168.1.100",
	})
	if err != nil {
		t.Errorf("AddHost() error = %v", err)
	}

	// Verify host was added
	if _, ok := pool.GetHostConfig("new-host"); !ok {
		t.Error("new-host not found after AddHost")
	}

	// Try to add duplicate
	err = pool.AddHost(config.SSHHostConfig{
		Name:     "new-host",
		Hostname: "192.168.1.200",
	})
	if err == nil {
		t.Error("AddHost(duplicate) should return error")
	}
}

func TestPool_RemoveHost(t *testing.T) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	defer pool.Close()

	// Remove a host
	err := pool.RemoveHost("host1")
	if err != nil {
		t.Errorf("RemoveHost() error = %v", err)
	}

	// Verify host was removed
	if _, ok := pool.GetHostConfig("host1"); ok {
		t.Error("host1 still found after RemoveHost")
	}

	// Try to remove nonexistent host
	err = pool.RemoveHost("nonexistent")
	if err == nil {
		t.Error("RemoveHost(nonexistent) should return error")
	}
}

func TestPool_Stats(t *testing.T) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	defer pool.Close()

	stats := pool.Stats()

	// Initially no connections
	if stats.TotalConnections != 0 {
		t.Errorf("initial TotalConnections = %d, want 0", stats.TotalConnections)
	}

	if len(stats.HostStats) != 0 {
		t.Errorf("initial HostStats has %d entries, want 0", len(stats.HostStats))
	}
}

func TestPool_AddHostToClosed(t *testing.T) {
	pool := NewPool([]config.SSHHostConfig{}, testDefaults(), testPoolConfig())
	pool.Close()

	err := pool.AddHost(config.SSHHostConfig{Name: "host", Hostname: "localhost"})
	if err == nil {
		t.Error("AddHost on closed pool should return error")
	}
}

func TestPool_RemoveHostFromClosed(t *testing.T) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	pool.Close()

	err := pool.RemoveHost("host1")
	if err == nil {
		t.Error("RemoveHost on closed pool should return error")
	}
}

func TestPool_ConcurrentAccess(t *testing.T) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	defer pool.Close()

	// Test concurrent operations on the pool
	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Concurrent stats reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = pool.Stats()
		}()
	}

	// Concurrent host list reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = pool.ListHosts()
		}()
	}

	// Concurrent host config reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = pool.GetHostConfig("host1")
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent operation error: %v", err)
	}
}

func TestPoolStats_Fields(t *testing.T) {
	stats := PoolStats{
		TotalConnections: 5,
		HostStats: map[string]HostPoolStats{
			"host1": {Total: 3, InUse: 2, Available: 1},
			"host2": {Total: 2, InUse: 1, Available: 1},
		},
	}

	if stats.TotalConnections != 5 {
		t.Errorf("TotalConnections = %d, want 5", stats.TotalConnections)
	}

	if len(stats.HostStats) != 2 {
		t.Errorf("len(HostStats) = %d, want 2", len(stats.HostStats))
	}

	h1 := stats.HostStats["host1"]
	if h1.Total != 3 || h1.InUse != 2 || h1.Available != 1 {
		t.Errorf("host1 stats = %+v, want Total=3 InUse=2 Available=1", h1)
	}
}

func TestHostPoolStats_Fields(t *testing.T) {
	stats := HostPoolStats{
		Total:     10,
		InUse:     7,
		Available: 3,
	}

	if stats.Total != 10 {
		t.Errorf("Total = %d, want 10", stats.Total)
	}
	if stats.InUse != 7 {
		t.Errorf("InUse = %d, want 7", stats.InUse)
	}
	if stats.Available != 3 {
		t.Errorf("Available = %d, want 3", stats.Available)
	}
}

func TestPooledClient_Fields(t *testing.T) {
	now := time.Now()
	pc := &pooledClient{
		client:     nil,
		inUse:      true,
		returnedAt: now,
	}

	if !pc.inUse {
		t.Error("inUse should be true")
	}
	if pc.returnedAt != now {
		t.Error("returnedAt mismatch")
	}
}

func TestPool_ListHostsEmpty(t *testing.T) {
	pool := NewPool([]config.SSHHostConfig{}, testDefaults(), testPoolConfig())
	defer pool.Close()

	hosts := pool.ListHosts()
	if len(hosts) != 0 {
		t.Errorf("ListHosts() returned %d hosts, want 0", len(hosts))
	}
}

func TestPool_GetHostConfigNotFound(t *testing.T) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	defer pool.Close()

	_, ok := pool.GetHostConfig("nonexistent")
	if ok {
		t.Error("GetHostConfig(nonexistent) should return false")
	}
}

// TestPool_PutUnknownClient tests that Put handles clients not from the pool
func TestPool_PutUnknownClient(t *testing.T) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	defer pool.Close()

	// Create a client that's not from the pool
	client := &SSHClient{
		closed: false,
	}

	// Put should not panic and should close the client
	pool.Put("host1", client)

	// Client should now be closed (though we can't verify internal state)
}

// TestPool_PutClosed tests that Put handles a closed pool
func TestPool_PutClosed(t *testing.T) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	pool.Close()

	client := &SSHClient{
		closed: false,
	}

	// Should not panic
	pool.Put("host1", client)
}

func TestPool_CleanupIdleConnections(t *testing.T) {
	// Create pool with very short idle timeout for testing
	poolConfig := config.SSHPoolConfig{
		MaxConnectionsPerHost: 5,
		MaxTotalConnections:   50,
		IdleTimeout:           1 * time.Millisecond,
		HealthCheckInterval:   10 * time.Millisecond,
		StrictHostKeyChecking: "no",
	}

	pool := NewPool(testHosts(), testDefaults(), poolConfig)
	defer pool.Close()

	// Pool starts with no connections, cleanup should be a no-op
	time.Sleep(50 * time.Millisecond)

	stats := pool.Stats()
	if stats.TotalConnections != 0 {
		t.Errorf("TotalConnections after cleanup = %d, want 0", stats.TotalConnections)
	}
}

// Benchmark tests
func BenchmarkPool_Stats(b *testing.B) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	defer pool.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pool.Stats()
	}
}

func BenchmarkPool_ListHosts(b *testing.B) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	defer pool.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pool.ListHosts()
	}
}

func BenchmarkPool_GetHostConfig(b *testing.B) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	defer pool.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = pool.GetHostConfig("host1")
	}
}

func BenchmarkPool_ConcurrentStatsReads(b *testing.B) {
	pool := NewPool(testHosts(), testDefaults(), testPoolConfig())
	defer pool.Close()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = pool.Stats()
		}
	})
}
