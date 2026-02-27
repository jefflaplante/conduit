# TypeScript Channel Adapters

This directory is reserved for **TypeScript/Node.js process adapters** that cannot be implemented natively in Go.

## Architecture

Conduit Go supports a **hybrid channel adapter system**:

1. **Native Go Adapters** (preferred): Direct integration in `internal/channels/<provider>/`
   - Better performance and reliability
   - Single binary deployment
   - Examples: Telegram

2. **TypeScript Process Adapters** (when necessary): External Node.js processes in this directory
   - For complex channel APIs that require TypeScript libraries
   - Communicates via JSON stdin/stdout
   - Examples: WhatsApp (future), Discord (future)

## When to Use Each

### Use Native Go Adapters When:
- ✅ Good Go library exists for the platform API
- ✅ Simple API integration requirements  
- ✅ Performance is critical
- ✅ Want single binary deployment

### Use TypeScript Process Adapters When:
- ⚠️ No suitable Go library exists
- ⚠️ Complex TypeScript ecosystem dependencies required
- ⚠️ Rapid prototyping needed
- ⚠️ Leveraging existing TypeScript channel code

## Future Additions

Planned TypeScript adapters:
- `whatsapp.js` - WhatsApp Web integration via Puppeteer
- `discord.js` - Discord bot with complex features

Current TypeScript adapters: **None** (Telegram migrated to native Go)

---

**Note**: The Telegram adapter was migrated from TypeScript to native Go for better performance. This directory is kept for future TypeScript adapters when needed.