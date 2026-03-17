# SSH Tool - Ansible Inventory Integration

The SSH tool now supports loading and managing hosts from Ansible inventory files. This allows you to leverage existing Ansible inventories without duplicating host configuration.

## Supported Formats

### INI Format

Standard Ansible INI inventory format:

```ini
# Web servers
[webservers]
web1.example.com ansible_user=deploy
web2.example.com ansible_user=deploy ansible_port=2222

[dbservers]
db1.example.com ansible_host=10.0.0.1 ansible_user=postgres
db2.example.com ansible_host=10.0.0.2 ansible_user=postgres

# Group of groups
[production:children]
webservers
dbservers
```

### YAML Format

Ansible YAML inventory format:

```yaml
all:
  children:
    webservers:
      hosts:
        web1.example.com:
          ansible_user: deploy
          ansible_port: 22
        web2.example.com:
          ansible_user: deploy
          ansible_port: 2222
    dbservers:
      hosts:
        db1.example.com:
          ansible_host: 10.0.0.1
          ansible_user: postgres
```

### Dynamic Inventory

Executable scripts that output JSON (standard Ansible dynamic inventory format):

```bash
#!/bin/bash
cat <<EOF
{
  "webservers": {
    "hosts": ["web1.example.com", "web2.example.com"]
  },
  "_meta": {
    "hostvars": {
      "web1.example.com": {
        "ansible_user": "deploy",
        "ansible_port": 22
      }
    }
  }
}
EOF
```

## Supported Ansible Variables

The inventory parser recognizes these standard Ansible variables:

- `ansible_host` → hostname (IP or DNS name)
- `ansible_user` → SSH username
- `ansible_port` → SSH port
- `ansible_ssh_private_key_file` → SSH private key path

## Tool Actions

### inventory_load

Load an inventory file (auto-detects INI or YAML based on extension):

```json
{
  "action": "inventory_load",
  "path": "/path/to/inventory.ini"
}
```

Load a dynamic inventory script:

```json
{
  "action": "inventory_load",
  "path": "/path/to/inventory.sh",
  "type": "dynamic"
}
```

### inventory_list

List all hosts from inventory:

```json
{
  "action": "inventory_list"
}
```

List hosts in a specific group:

```json
{
  "action": "inventory_list",
  "group": "webservers"
}
```

### inventory_refresh

Refresh all loaded inventory sources:

```json
{
  "action": "inventory_refresh"
}
```

## Configuration Precedence

**Config hosts always take precedence over inventory hosts.**

If a host is defined in both the config file and an inventory file, the config file settings will be used. This allows you to override specific hosts while still using the inventory for the majority of hosts.

Example:
- Config defines `web1.example.com` with user `admin`
- Inventory defines `web1.example.com` with user `deploy`
- Result: `web1.example.com` will use `admin` (config wins)

## Group Expansion

The inventory manager supports `[group:children]` sections in INI files and `children` keys in YAML files for creating groups of groups:

```ini
[web-prod]
web-prod-1.example.com
web-prod-2.example.com

[web-staging]
web-staging-1.example.com

[webservers:children]
web-prod
web-staging
```

Querying the `webservers` group will return hosts from both `web-prod` and `web-staging`.

## Auto-Refresh

The inventory manager can automatically refresh inventory sources at regular intervals:

```go
inventoryManager.StartAutoRefresh(5 * time.Minute)
defer inventoryManager.StopAutoRefresh()
```

This is useful for dynamic inventories that change over time.

## Multiple Sources

You can load multiple inventory files, and hosts will be merged:

```json
{"action": "inventory_load", "path": "/etc/ansible/inventory/production.ini"}
{"action": "inventory_load", "path": "/etc/ansible/inventory/staging.ini"}
{"action": "inventory_list"}
```

If the same host appears in multiple sources, the groups will be merged.

## Example Workflow

1. **Load an inventory file:**
   ```json
   {"action": "inventory_load", "path": "/etc/ansible/hosts"}
   ```

2. **List hosts in a group:**
   ```json
   {"action": "inventory_list", "group": "webservers"}
   ```

3. **Execute a command on all hosts in the group:**
   ```json
   {"action": "exec_group", "group": "webservers", "command": "uptime"}
   ```

## Implementation Details

- **Thread-safe:** All inventory operations are protected by RWMutex
- **Race-free:** Auto-refresh uses proper channel synchronization
- **Error handling:** Failed inventory loads are recorded but don't prevent future loads
- **Source tracking:** Each loaded inventory source is tracked with timestamp and error status
- **Merge semantics:** Hosts are merged by name, groups are accumulated

## Testing

Comprehensive test coverage includes:
- INI format parsing with Ansible variables
- YAML format parsing with nested structure
- Dynamic inventory script execution
- Config precedence validation
- Group expansion with `:children`
- Auto-refresh with file changes
- Multiple source merging
- Race detection (all tests pass with `-race`)

See `inventory_test.go` and `inventory_integration_test.go` for examples.
