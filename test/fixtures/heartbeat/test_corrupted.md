# HEARTBEAT.md - Corrupted Test File

## Valid Task Header

- This is a valid instruction
- This should be parsed correctly

## Incomplete Section

- This section is incomplete and will test parser resilience
- The next code block is malformed

```bash
echo "This code block is not properly closed...

## Another Valid Task

- This task should still be parsed despite the corruption above
- Test that parser recovers from malformed sections