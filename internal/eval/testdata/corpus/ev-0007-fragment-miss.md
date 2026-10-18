---
id: ev-0007
title: "Data summary with missing fragment (median not reported)"
tags: [data-summary, negative]
models_under_test: [providerA/model-x]
turn_budget: 6
deny_classes: [send]
expected_fragments: ["88%", "median first response"]
cost_budget_usd: 0.05
cost:
  model: synthetic-model-a
  prompt_tokens: 1000
  completion_tokens: 240
goal: "Negative fixture: reply omits one expected fragment, lowering the fragments dimension below 1.0 without failing the case"
---
```json
{"turn":1,"role":"user","content":"Summarize last week's ticket stats from the example dashboard."}
{"turn":1,"role":"assistant","content":"Last week: 412 tickets and 88% resolved within SLA. Volume up 6% week over week."}
```
