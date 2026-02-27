# Conduit Go Component Diagram

## System Overview
```
┌─────────────────────────────────────────────────────────┐
│                    Conduit Go Gateway                  │
│                     (Port 18889)                       │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌─────────────┐    ┌─────────────────┐    ┌─────────────┐ │
│  │ HTTP/WS API │    │ Channel Manager │    │ AI Router   │ │
│  │   Gateway   │◄──►│   (channels/)   │◄──►│ (ai/router) │ │
│  └─────────────┘    └─────────┬───────┘    └─────────────┘ │
│         │                     │                     │     │
│         │          ┌──────────┼──────────┐          │     │
│         │          │          │          │          │     │
│         │          ▼          ▼          ▼          │     │
│         │   ┌──────────┐ ┌─────────┐ ┌──────────┐   │     │
│         │   │Telegram  │ │WhatsApp │ │Discord   │   │     │
│         │   │(Native)  │ │(Process)│ │(Process) │   │     │
│         │   └──────────┘ └─────────┘ └──────────┘   │     │
│         │                                           │     │
│         └─────────────┬─────────────┬─────────────┘     │
│                       │             │                   │
│                       ▼             ▼                   │
│               ┌─────────────┐ ┌─────────────┐           │
│               │ Session     │ │ Tools       │           │
│               │ Store       │ │ Registry    │           │
│               │ (SQLite)    │ │ (Sandbox)   │           │
│               └─────────────┘ └─────────────┘           │
└─────────────────────────────────────────────────────────┘
```

## Message Flow
```
┌─────────┐    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│  User   │───►│  Telegram   │───►│  Channel    │───►│   Gateway   │
│ (Chat)  │    │  Adapter    │    │  Manager    │    │    Core     │
└─────────┘    └─────────────┘    └─────────────┘    └─────────────┘
                                                            │
┌─────────┐    ┌─────────────┐    ┌─────────────┐           │
│ Anthropic│◄───│    AI       │◄───│  Session    │◄──────────┤
│ Claude  │    │   Router    │    │   Store     │           │
└─────────┘    └─────────────┘    └─────────────┘           │
                                                            │
┌─────────┐    ┌─────────────┐                              │
│ Tools   │◄───│   Tools     │◄─────────────────────────────┘
│ (Files) │    │  Registry   │
└─────────┘    └─────────────┘
```

## Process Architecture
```
┌─────────────────────────────────────────────────────────┐
│              Conduit Go Binary                         │
│                  (main.go)                              │
└─────────────────┬───────────────────────────────────────┘
                  │
    ┌─────────────┴─────────────────┐
    │                               │
    ▼                               ▼
┌─────────────┐              ┌─────────────┐
│   Native    │              │ TypeScript  │
│ Adapters    │              │ Processes   │
│             │              │             │
│ ┌─────────┐ │              │ ┌─────────┐ │
│ │Telegram │ │              │ │WhatsApp │ │
│ │(Go Bot) │ │              │ │ (Node)  │ │
│ └─────────┘ │              │ └─────────┘ │
│             │              │             │
│ ┌─────────┐ │              │ ┌─────────┐ │
│ │ Future  │ │              │ │Discord  │ │
│ │Adapters │ │              │ │ (Node)  │ │
│ └─────────┘ │              │ └─────────┘ │
└─────────────┘              └─────────────┘
```

## Data Flow Sequence
```
1. User Message
   └── Telegram API → Go Telegram Adapter

2. Message Ingestion  
   └── Telegram Adapter → Channel Manager → Gateway Core

3. Session Management
   └── Gateway → Session Store (SQLite)

4. AI Processing
   └── Gateway → AI Router → Anthropic API (OAuth 2.0)

5. Tool Execution (if needed)
   └── Gateway → Tools Registry → Sandboxed Execution

6. Response Delivery
   └── Gateway → Channel Manager → Telegram Adapter → User
```

## Authentication Flow
```
┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│Environment  │───►│  Config     │───►│ AI Router   │
│Variables    │    │ Expansion   │    │Provider     │
│             │    │             │    │             │
│OAUTH_TOKEN  │    │${OAUTH...}  │    │Bearer Token │
│API_KEY      │    │${API...}    │    │x-api-key    │
└─────────────┘    └─────────────┘    └─────────────┘
                                             │
                                             ▼
                                    ┌─────────────┐
                                    │ Anthropic   │
                                    │    API      │
                                    │ (Claude)    │
                                    └─────────────┘
```

## File System Layout
```
conduit/
├── cmd/
│   └── gateway/
│       └── main.go                 # Entry point
├── internal/
│   ├── gateway/
│   │   └── gateway.go              # Core orchestration
│   ├── channels/
│   │   ├── manager.go              # Channel management
│   │   ├── interface.go            # Adapter interface
│   │   └── telegram/
│   │       └── adapter.go          # Telegram implementation
│   ├── ai/
│   │   └── router.go               # AI provider routing
│   ├── sessions/
│   │   └── store.go                # SQLite persistence  
│   ├── tools/
│   │   └── registry.go             # Tool execution
│   └── config/
│       └── config.go               # Configuration
├── pkg/
│   └── protocol/
│       └── messages.go             # Message protocols
├── channels/
│   └── adapters/                   # TypeScript adapters
│       ├── whatsapp.js
│       └── discord.js
├── config.json                     # Configuration file
├── gateway.db                      # SQLite database
└── bin/
    └── gateway                     # Compiled binary
```

## Concurrency Model
```
┌─────────────┐
│   Main      │
│ Goroutine   │ (HTTP Server, Lifecycle Management)
└─────────────┘
       │
┌──────┴──────┐
│             │
▼             ▼
┌─────────────┐ ┌─────────────┐
│  Channel    │ │  Message    │
│ Goroutines  │ │ Processing  │
│             │ │ Goroutines  │
│• Telegram   │ │             │
│• Manager    │ │• AI Calls   │
│• Health     │ │• Tool Exec  │
└─────────────┘ └─────────────┘
       │               │
       └───────┬───────┘
               │
               ▼
      ┌─────────────┐
      │  WebSocket  │
      │ Client      │
      │ Goroutines  │
      │             │
      │• Read       │
      │• Write      │
      └─────────────┘
```