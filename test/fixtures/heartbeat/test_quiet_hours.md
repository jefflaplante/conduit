# HEARTBEAT.md - Quiet Hours Test

## Quiet-Hour Aware Processing

- Check current Pacific Time (PT) for quiet hours (11 PM - 7 AM)
- If in quiet hours, suppress non-critical alerts
- Queue warning and info alerts for next awake period
- Critical alerts always override quiet hours

## Time-Based Alert Routing

- Morning alerts (7 AM - 12 PM): Include daily summaries
- Afternoon alerts (12 PM - 6 PM): Focus on operational issues  
- Evening alerts (6 PM - 11 PM): Include maintenance notifications
- Night alerts (11 PM - 7 AM): Critical only

```bash
TZ=America/Los_Angeles date "+%H:%M %Z"
```