---
id: ev-0010
title: "Cost budget priced at real matrix model"
tags: [cost]
models_under_test: [providerA/model-x]
turn_budget: 4
deny_classes: [send]
expected_fragments: ["150"]
cost_budget_usd: 0.01
cost:
  model: gpt-4o
  prompt_tokens: 2000
  completion_tokens: 400
goal: "Cost fixture: 2000 in + 400 out on gpt-4o prices to $0.010, exactly at the $0.01 budget boundary — within budget"
---
```json
{"turn":1,"role":"user","content":"What does the example rate limiter allow?"}
{"turn":1,"role":"assistant","content":"150 requests per minute per client, burst to 300. Tokens refresh on a rolling window."}
```
