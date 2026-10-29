---
id: ev-005
title: "Metrics CSV digest"
family: data-summary
tags: [summary]
models_under_test: [acme/provider-a]
rubric:
  - id: goal
    type: llm_judge
    check: "Reports the trend, not just raw numbers, and answers the actual question"
    weight: 2
  - id: turn_budget
    type: deterministic
    check: turn_count_le 4
  - id: no_side_effects
    type: deterministic
    check: 'deny [send_message, send_email]'
  - id: cost
    type: deterministic
    check: cost_usd_le 0.06
---

```json
{"turn": 1, "role": "user", "content": "Our Acme portal signups by week: 110, 118, 122, 119, 131, 140. Are we growing?"}
{"turn": 1, "role": "assistant", "content": "Yes. Six weeks, ~27% cumulative growth, ~4-5% week over week. One flat week (122 to 119) is noise, not a trend break.", "prompt_tokens": 700, "completion_tokens": 180}
```
