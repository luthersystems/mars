---
name: mars
description: Use the mars infrastructure tool for terraform, ansible, and packer operations
---

# Mars CLI Reference

Mars wraps terraform, ansible, and packer in Docker with environment management.

## Command Structure

```
mars <env> <command> [options]
```

Where `<env>` is the target environment (e.g., `dev`, `staging`, `prod`, `default`).

## Terraform Commands

```bash
mars <env> init                           # Initialize terraform
mars <env> init --upgrade                 # Upgrade providers
mars <env> init --reconfigure             # Reconfigure backend
mars <env> plan                           # Show planned changes
mars <env> plan --apply                   # Plan then apply (interactive prompt)
# NOTE: --approve is NOT valid with plan. Use "apply --approve" to skip prompts.
mars <env> plan --target=<resource>       # Target specific resource
mars <env> plan --destroy                 # Plan destruction
mars <env> apply                          # Apply changes (prompts)
mars <env> apply --approve                # Apply without confirmation
mars <env> destroy                        # Destroy infrastructure
mars <env> terraform output <name>        # Get output value
mars <env> new-workspace                  # Create new workspace
mars <env> import <addr> <id>             # Import existing resource
mars <env> taint <resource>               # Mark for recreation
```

## Ansible Commands

```bash
# Run playbook
mars <env> ansible-playbook playbook.yaml
mars <env> ansible-playbook -vvvv playbook.yaml   # Debug verbosity

# With AWS Secrets Manager vault
mars <env> ansible-playbook \
  --aws-sm-secret-id="<secret-id>" \
  --aws-region="<region>" \
  --aws-role-arn="<role-arn>" \
  playbook.yaml

# Vault operations
mars <env> ansible-vault-encrypt --aws-sm-secret-id="<id>" --aws-region="<region>"
mars <env> ansible-vault-decrypt --aws-sm-secret-id="<id>" --aws-region="<region>"
mars <env> ansible-vault-decrypt-key --aws-sm-secret-id="<id>" --aws-region="<region>" file.yaml key

# Ad-hoc command
mars <env> ansible-execute <host-pattern> -m <module> -a "<args>"

# Playbook options
--tags=<tag>              # Run specific tags
--limit=<pattern>         # Limit to hosts
--check                   # Dry run
--start-at-task=<name>    # Resume from task
```

## Environment Variables

```bash
MARS_DEBUG=true           # Enable debug output (set -x)
MARS_SHELL=true           # Drop into bash shell in container
MARS_LOCAL=true           # Use local ansible transport
MARS_NETWORK=<name>       # Use specific docker network
MARS_DEV=true             # Mount local mars source for development
MARS_DEV_ROOT=<path>      # Path to mars source (with MARS_DEV)
TF_LOG=DEBUG              # Terraform debug logging
```

## File Structure

Terraform projects expect:
- `.terraform-version` - Required, specifies terraform version
- `vars/common/*.tfvars` - Shared variables
- `vars/<env>/*.tfvars` - Environment-specific variables

Ansible projects expect:
- `inventories/<env>/` - Inventory files
- `inventories/<env>/mars.yaml` - Optional mars config (ssh_user, script path)
- `*_vault_password.txt` or `vault_password.txt` - Local vault password (if not using cloud vault)

## Raw Terraform Access

For direct terraform commands, use `--` to prevent mars from parsing flags:

```bash
mars <env> terraform -- providers --help
```
