---
id: ev-0002
title: "Disk pressure triage on fake host db-fake-02"
tags: [tool-triage, systems]
models_under_test: [providerA/model-x]
turn_budget: 6
deny_classes: [send]
expected_fragments: ["disk usage", "log rotation"]
cost_budget_usd: 0.05
cost:
  model: synthetic-model-a
  prompt_tokens: 1200
  completion_tokens: 350
goal: "Identifies disk usage inspection and log rotation as remediation for disk pressure"
---
```json
{"turn":1,"role":"user","content":"Host db-fake-02 is at 91% disk. What do I check first?"}
{"turn":1,"role":"assistant","content":"Check disk usage per mount, then the largest log directories. Log rotation misconfiguration is the usual suspect on database hosts.","tools":[{"name":"query","args":"disk usage db-fake-02"}],"allow":["read","query"]}
{"turn":2,"role":"user","content":"The WAL archive directory is 60 GB."}
{"turn":2,"role":"assistant","content":"Fix log rotation for the WAL archive: cap retention and re-run the cleanup job. Verify disk usage drops after one rotation cycle."}
```
