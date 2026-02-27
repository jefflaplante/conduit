# HEARTBEAT.md - Critical Alert Test

## Critical Alert Processing

- Check for critical system alerts immediately
- If critical alerts exist, deliver to all channels immediately
- Do not wait for quiet hours - critical alerts override time restrictions
- Log all critical alert deliveries with timestamps

```bash
echo "Checking critical alerts..."
find /tmp/alerts -name "*.critical" -type f
```

## Emergency Escalation

- If critical alerts remain undelivered for more than 5 minutes, escalate
- Send backup notifications via secondary channels
- Alert system administrators directly