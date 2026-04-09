# z.ai / Zhipu AI (GLM) Provider Setup

z.ai is the international platform for Zhipu AI, China's leading independent LLM company. It provides OpenAI-compatible APIs for the GLM (General Language Model) series.

## Quick Start

### 1. Get API Key

1. Register at [z.ai](https://z.ai)
2. Generate an API key from your dashboard

### 2. Set Environment Variable

```bash
export Z_AI_API_KEY="your-api-key-here"
```

Or add to your secrets file.

### 3. Add Provider to Config

**GLM Coding Plan (recommended):**
```json
{
  "ai": {
    "providers": [
      {
        "name": "z-ai",
        "type": "openai",
        "base_url": "https://api.z.ai/api/coding/paas/v4",
        "api_key": "${Z_AI_API_KEY}",
        "model": "glm-4-flash"
      }
    ]
  }
}
```

**Standard API (pay-as-you-go):**
```json
{
  "base_url": "https://api.z.ai/api/paas/v4"
}
```

### 4. Add Model Aliases (Optional)

```json
{
  "ai": {
    "model_aliases": {
      "glm": "z-ai/glm-4-flash",
      "glm-plus": "z-ai/glm-4-plus",
      "glm-vision": "z-ai/glm-4v-flash"
    }
  }
}
```

## API Endpoints

| Plan | Base URL |
|------|----------|
| GLM Coding Plan ($10/mo) | `https://api.z.ai/api/coding/paas/v4` |
| Standard (pay-as-you-go) | `https://api.z.ai/api/paas/v4` |

**Important:** Use the endpoint that matches your subscription. Using the wrong endpoint results in error 1113 ("Insufficient balance").

## Available Models

| Model | Description | Use Case |
|-------|-------------|----------|
| `glm-4-flash` | Fast, cost-effective | General purpose, high throughput |
| `glm-4-plus` | Enhanced capabilities | Complex reasoning |
| `glm-4-air` | Balanced performance | Standard tasks |
| `glm-4v-flash` | Vision + text (fast) | Image analysis |
| `glm-4v-plus` | Vision + text (enhanced) | Advanced vision |
| `glm-4-long` | Long context (1M tokens) | Document processing |
| `cogview-3-flash` | Text-to-image | Image generation |

## Usage

Switch to z.ai model:

```
/model z-ai/glm-4-flash
```

Or using an alias:

```
/model glm
```

## Full Config Example

```json
{
  "ai": {
    "default_provider": "anthropic",
    "providers": [
      {
        "name": "anthropic",
        "type": "anthropic",
        "api_key": "${ANTHROPIC_API_KEY}",
        "model": "claude-sonnet-4-6"
      },
      {
        "name": "z-ai",
        "type": "openai",
        "base_url": "https://api.z.ai/api/coding/paas/v4",
        "api_key": "${Z_AI_API_KEY}",
        "model": "glm-4-flash"
      }
    ],
    "model_aliases": {
      "haiku": "claude-haiku-4-5-20251001",
      "sonnet": "claude-sonnet-4-6",
      "opus": "claude-opus-4-6",
      "glm": "z-ai/glm-4-flash",
      "glm-plus": "z-ai/glm-4-plus"
    }
  }
}
```

## Troubleshooting

**"unauthorized" or 401 errors:**
- Verify `Z_AI_API_KEY` is set correctly
- Check API key hasn't expired in z.ai dashboard

**"Insufficient balance" (error 1113):**
- You're using the wrong endpoint for your subscription
- Coding Plan subscribers: use `/api/coding/paas/v4`
- Pay-as-you-go: use `/api/paas/v4` and add credits

**Model not found:**
- Use explicit provider prefix: `z-ai/model-name`
- Check model name spelling

**Timeout errors:**
- z.ai servers may have higher latency from some regions
- Consider increasing timeout settings if needed

## See Also

- [Configuration Reference](configuration.md) - Full config options
- [AI Providers](configuration.md#ai-providers) - Provider configuration details
