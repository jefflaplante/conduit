---
id: ev-0001
title: "Interface error triage on lab switch sw-fake-01"
tags: [tool-triage, networking]
models_under_test: [providerA/model-x]
turn_budget: 8
deny_classes: [send, exec]
expected_fragments: ["crc", "error counters"]
cost_budget_usd: 0.05
cost:
  model: synthetic-model-a
  prompt_tokens: 1500
  completion_tokens: 400
goal: "Directs the user to check interface error counters and logs before any config change"
---
```json
{"turn":1,"role":"user","content":"Lab switch sw-fake-01 drops connections every few minutes. Where do I start?"}
{"turn":1,"role":"assistant","content":"Check the interface error counters first — look for CRC errors on the uplink ports. Then pull the logs around a drop window.","tools":[{"name":"read","args":"show interface counters example"}],"allow":["read","query"]}
{"turn":2,"role":"user","content":"Counters show CRC errors climbing on port 5."}
{"turn":2,"role":"assistant","content":"CRC errors climbing on one port usually means a bad cable or transceiver. Swap the cable on port 5 and watch the counters."}
```
