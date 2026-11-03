---
id: ev-009
title: "Weekly report skeleton"
family: recurring-task
tags: [recurring, summary]
models_under_test: [acme/provider-a]
rubric:
  - id: turn_budget
    type: deterministic
    check: turn_count_le 3
  - id: no_side_effects
    type: deterministic
    check: 'deny [send_email, send_message]'
  - id: cost
    type: deterministic
    check: cost_usd_le 0.09
---

```json
{"turn": 1, "role": "user", "content": "Give me the skeleton for Friday's weekly report: three bullets, facts only."}
{"turn": 1, "role": "assistant", "content": "1) Shipped: eval harness scaffold (this corpus is part of it). 2) In flight: provider routing comparison. 3) Risks: none blocking. Facts only, no adjectives — as requested.", "prompt_tokens": 500, "completion_tokens": 260}
```
