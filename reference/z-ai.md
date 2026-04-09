# z.ai Provider Setup

z.ai is an AI inference platform from Zhipu AI, China's leading independent LLM company. It provides OpenAI-compatible APIs for the GLM (General Language Model) series.

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

```json
{
  "ai": {
    "providers": [
      {
        "name": "z-ai",
        "type": "openai",
        "base_url": "https://api.z.ai/api/paas/v4",
        "api_key": "${Z_AI_API_KEY}",
        "model": "glm-5-turbo"
      }
    ]
  }
}
```

### 4. Add Model Aliases (Optional)

```json
{
  "ai": {
    "model_aliases": {
      "glm": "z-ai/glm-5-turbo",
      "glm-vision": "z-ai/glm-5v-turbo",
      "glm-flagship": "z-ai/glm-5.1"
    }
  }
}
```

## API Details

| Setting | Value |
|---------|-------|
| Base URL | `https://api.z.ai/api/paas/v4` |
| Coding Endpoint | `https://api.z.ai/api/coding/paas/v4` (requires GLM Coding Plan, $10/month) |
| Authentication | Bearer token via API key |
| Format | OpenAI-compatible |

## Available Models

| Model | Description | Use Case |
|-------|-------------|----------|
| `glm-5.1` | Flagship foundation model | Complex reasoning, analysis |
| `glm-5-turbo` | Optimized turbo variant | General purpose, fast |
| `glm-5v-turbo` | Multimodal (vision + text) | Image analysis |
| `glm-5` | Base model | Standard tasks |
| `glm-4.6v` | Vision reasoning (100B-class) | Advanced vision |
| `glm-image` | Text-to-image generation | Image creation |
| `glm-ocr` | Optical character recognition | Document processing |

## Usage

Switch to z.ai model:

```
/model z-ai/glm-5-turbo
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
        "base_url": "https://api.z.ai/api/paas/v4",
        "api_key": "${Z_AI_API_KEY}",
        "model": "glm-5-turbo"
      }
    ],
    "model_aliases": {
      "haiku": "claude-haiku-4-5-20251001",
      "sonnet": "claude-sonnet-4-6",
      "opus": "claude-opus-4-6",
      "glm": "z-ai/glm-5-turbo",
      "glm-vision": "z-ai/glm-5v-turbo"
    }
  }
}
```

## Troubleshooting

**"unauthorized" or 401 errors:**
- Verify `Z_AI_API_KEY` is set correctly
- Check API key hasn't expired in z.ai dashboard

**Model not found:**
- Use explicit provider prefix: `z-ai/model-name`
- Check model name spelling matches z.ai documentation

**Timeout errors:**
- z.ai servers are in China; latency may be higher from other regions
- Consider increasing timeout settings if needed

## See Also

- [Configuration Reference](configuration.md) - Full config options
- [AI Providers](configuration.md#ai-providers) - Provider configuration details
