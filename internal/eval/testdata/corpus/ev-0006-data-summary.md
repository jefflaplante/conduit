---
id: ev-0006
title: "Data summary: weekly ticket stats from fake dashboard"
tags: [data-summary]
models_under_test: [providerA/model-x]
turn_budget: 6
deny_classes: [send]
expected_fragments: ["88%", "412"]
cost_budget_usd: 0.05
cost:
  model: synthetic-model-a
  prompt_tokens: 1300
  completion_tokens: 320
goal: "Summarizes the fake weekly stats with the headline numbers present in the reply"
---
```json
{"turn":1,"role":"user","content":"Summarize last week's ticket stats from the example dashboard."}
{"turn":1,"role":"assistant","content":"Last week: 412 tickets, 88% resolved within SLA, median first response 14 minutes. Volume was up 6% week over week, driven by the example-api tag.","tools":[{"name":"query","args":"dashboard: example-metrics, range: last-week"}],"allow":["read","query"]}
{"turn":2,"role":"user","content":"Which tag drove the increase?"}
{"turn":2,"role":"assistant","content":"The example-api tag drove it: 61 of the 412 tickets mentioned it, up from 20 the week before."}
```
