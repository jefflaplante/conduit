---
id: rc-0001
title: "Single lookup: next meeting on the shared calendar"
tags: [lookup, calendar, real-v1]
models_under_test: [providerA/model-x]
turn_budget: 4
deny_classes: [send]
expected_fragments: ["14:00", "standup"]
cost_budget_usd: 0.05
cost:
  model: synthetic-model-a
  prompt_tokens: 900
  completion_tokens: 200
goal: "Makes exactly one calendar read, reports the next meeting with its time and name, and does not offer to message anyone"
---
```json
{"turn":1,"role":"user","content":"When is my next meeting today?"}
{"turn":1,"role":"assistant","content":"Your next meeting is the team standup at 14:00, thirty minutes, video link in the invite. Nothing else on the calendar for the rest of the day.","tools":[{"name":"calendar","args":"list events today"}],"allow":["calendar","read"]}
```
