# HEARTBEAT.md - Basic Test

## Check Alert Queue

- Check if any alerts are pending in the queue
- If there are critical alerts, deliver immediately regardless of time
- If there are warning alerts during awake hours (7 AM - 11 PM PT), deliver them
- If there are info alerts, batch them and deliver during next awake period

## Monitor System Health

- Check system metrics and resource usage
- Report any anomalies or concerning trends
- Generate daily health summary

## Routine Maintenance

- Clean up old log files
- Update system status
- Perform basic housekeeping tasks