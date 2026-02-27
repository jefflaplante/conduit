# Telegram Pairing System

This document describes the implementation of the Telegram pairing system for Conduit.

## Overview

The pairing system ensures that only authorized users can interact with the Conduit Telegram bot. When a new user attempts to send a message, they receive a pairing code that must be approved by an administrator before they can use the bot.

## Architecture

### Core Components

1. **PairingManager** (`pairing.go`): Core business logic for pairing operations
2. **Adapter Integration**: Modified `adapter.go` to check pairing status for all incoming messages
3. **Database Schema**: `telegram_pairings` table (migration v3) stores pairing codes and status

### Database Schema

```sql
CREATE TABLE telegram_pairings (
    code VARCHAR(36) PRIMARY KEY,        -- UUID-based pairing code
    user_id TEXT NOT NULL,              -- Telegram user ID (chat ID)
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NOT NULL,      -- 1 hour expiration
    is_active BOOLEAN DEFAULT 1,        -- 0 = approved/used, 1 = pending
    metadata TEXT DEFAULT '{}'
);
```

## Key Functions

### PairingManager Methods

- `GeneratePairingCode(userID string) (string, error)`: Creates new UUID-based pairing code
- `ValidatePairingCode(code string) (*PairingRecord, error)`: Validates code and checks expiration
- `ApprovePairing(code string) error`: Approves pairing (marks code as inactive)
- `IsUserPaired(userID string) (bool, error)`: Checks if user has approved pairing
- `HandlePairingForUser(bot, userID, chatID)`: Complete pairing flow handler

### Adapter Methods

- `ApprovePairingCode(code string) error`: Public API for approving codes
- `GetPairingStats() (map[string]interface{}, error)`: Returns pairing statistics
- `CleanupExpiredPairingCodes() error`: Removes expired codes from database
- `IsPairingEnabled() bool`: Check if pairing is active

## Message Flow

### For Unpaired Users

1. User sends message to bot
2. `handleUpdate()` calls `HandlePairingForUser()`
3. System checks if user has approved pairing
4. If not paired:
   - Check for existing active pairing code
   - Generate new code if none exists or expired
   - Send pairing code message to user
   - Block message from reaching Conduit

### For Paired Users

1. User sends message to bot
2. `handleUpdate()` calls `HandlePairingForUser()`
3. System confirms user is paired
4. Message proceeds to Conduit for processing

### Pairing Approval Process

1. Administrator receives pairing code from user
2. Administrator approves via API/CLI: `ApprovePairingCode(code)`
3. System marks code as approved (is_active = 0)
4. System sends approval notification to user
5. User can now send messages normally

## Message Templates

### Pairing Code Request
```
🔐 *Conduit Pairing Required*

To use this bot, please provide this pairing code to your Conduit admin:

`[UUID_CODE]`

⏰ This code expires in 1 hour.
📞 If you don't have access to Conduit, contact your administrator.
```

### Pairing Approved
```
✅ *Pairing Approved*

Welcome to Conduit! You can now send messages and interact with the assistant.

🎉 Your account has been successfully paired.
```

### Code Expired
```
⏰ *Pairing Code Expired*

Your pairing code has expired. A new pairing code has been generated:

`[NEW_UUID_CODE]`

⏰ This code expires in 1 hour.
```

## Security Features

1. **UUID-based codes**: Secure, unpredictable pairing codes
2. **Expiration**: All codes expire after 1 hour
3. **One-time use**: Codes are deactivated after approval
4. **User isolation**: Each user gets their own pairing code
5. **Database cleanup**: Expired codes are automatically cleaned up

## Integration Points

### Factory Pattern
```go
// Create factory with database support
channelManager.RegisterFactory(telegram.NewFactoryWithDB(sessionStore.DB()))
```

### Gateway Integration
The pairing system is automatically initialized when the Telegram adapter is created with database support in the gateway.

### API Access
External systems can approve pairings via:
```go
adapter.ApprovePairingCode(code string) error
```

## Configuration

Pairing is automatically enabled when:
1. Telegram adapter is created with database support
2. Database migrations have run (includes telegram_pairings table)

No additional configuration is required.

## Monitoring

### Statistics Available
- Active pairing codes count
- Expired codes count  
- Total approved pairings
- Unique paired users count

### Logging
All pairing operations are logged with appropriate privacy protection:
- Code generation events
- Approval events
- Pairing check results
- Error conditions

## Error Handling

- **Database errors**: Graceful degradation with error messages
- **Invalid codes**: Clear error messages for administrators
- **Expired codes**: Automatic new code generation
- **Missing pairing manager**: Safe fallback (no pairing enforcement)

## Maintenance

### Cleanup Operations
```go
// Remove expired pairing codes
adapter.CleanupExpiredPairingCodes()

// Get current statistics
stats, err := adapter.GetPairingStats()
```

### Troubleshooting
1. Check if pairing is enabled: `adapter.IsPairingEnabled()`
2. Verify database connectivity and migrations
3. Review logs for pairing-related errors
4. Check pairing statistics for system health

## Future Enhancements

1. **Group Chat Support**: Currently designed for direct messages
2. **Bulk Approval**: Approve multiple codes at once
3. **Whitelist Mode**: Pre-approve specific user IDs
4. **Audit Trail**: Track all pairing operations
5. **Rate Limiting**: Limit pairing code generation per user