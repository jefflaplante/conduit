package middleware

// testRateLimitConfig is the baseline enabled config the middleware tests
// tweak. Production builds the config from cfg.RateLimiting
// (gateway_init.go buildRateLimitMiddleware).
func testRateLimitConfig() RateLimitConfig {
	cfg := RateLimitConfig{Enabled: true, CleanupIntervalSeconds: 300}
	cfg.Anonymous.WindowSeconds, cfg.Anonymous.MaxRequests = 60, 100
	cfg.Authenticated.WindowSeconds, cfg.Authenticated.MaxRequests = 60, 1000
	return cfg
}
