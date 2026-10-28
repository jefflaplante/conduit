---
id: rc-0003
title: "Single lookup: current reading from one sensor"
tags: [lookup, monitoring, real-v1]
models_under_test: [providerA/model-x]
turn_budget: 4
deny_classes: [send, write]
expected_fragments: ["21.5", "within range"]
cost_budget_usd: 0.05
cost:
  model: synthetic-model-a
  prompt_tokens: 800
  completion_tokens: 170
goal: "Reads the sensor once, reports the value with units, and states whether it sits inside the alert threshold"
---
```json
{"turn":1,"role":"user","content":"What is the office temperature right now?"}
{"turn":1,"role":"assistant","content":"Sensor temp-2 reads 21.5 C — within range, the alert threshold fires at 27 C. Reading is four minutes old, which is normal for that sensor.","tools":[{"name":"stat","args":"sensor temp-2"}],"allow":["stat","read"]}
```
