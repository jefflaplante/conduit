package gateway

// conduit-31jg.25: synchronized accessors and lifecycle signal for Client.

// SessionKey returns the client's active session key. Safe for concurrent use.
func (c *Client) SessionKey() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionKey
}

// SetSessionKey updates the client's active session key. Safe for concurrent use.
func (c *Client) SetSessionKey(key string) {
	c.mu.Lock()
	c.sessionKey = key
	c.mu.Unlock()
}

// Done returns a channel that is closed once the client's read loop has
// exited (peer gone, read error, deadline). The send-pump selects on it so
// it does not outlive the connection.
func (c *Client) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done == nil {
		c.done = make(chan struct{})
	}
	return c.done
}

// markDone closes Done(). Idempotent.
func (c *Client) markDone() {
	c.doneOnce.Do(func() {
		c.mu.Lock()
		if c.done == nil {
			c.done = make(chan struct{})
		}
		close(c.done)
		c.mu.Unlock()
	})
}
