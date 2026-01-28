# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Workflow

- Always create a feature branch and submit a PR to `main` - never push directly to `main`
- A human will review the PR before merging
- GitHub Actions automatically build and push Docker images to DockerHub for tagged releases

## Overview

Mars is Luther Systems' infrastructure management tool - a Docker-based wrapper around Terraform, Ansible, and Packer. It provides environment-aware infrastructure operations with integrated secret management (AWS Secrets Manager, Azure KeyVault).

## Build Commands

```bash
make                    # Build the Docker image (luthersystems/mars)
make push               # Push to registry
make clean              # Remove build artifacts
```

## AWS Credentials Setup

Before running mars, set up AWS credentials using the `aws_login` helper:

```bash
aws_admin                 # Alias for: aws_login admin (assumes admin role with MFA)
aws_jump                  # Alias for: aws_login jump (assumes jump role with MFA)
aws_login <role>          # Generic: assumes specified role with MFA, 1-hour session
```

These use `speculate` to assume IAM roles with MFA and set AWS environment variables.

## Using the Mars CLI

Mars runs inside Docker. On macOS, use `mars_macos.sh` (typically aliased as `mars`).

Before running mars commands, source the vault reference file for your environment:
```bash
source vars/test/vault-ref    # Sets AWS_SM_SECRET_ID, AWS_REGION, etc.
```

```bash
# Terraform operations (most common)
mars <env> init                           # Initialize terraform
mars <env> plan                           # Plan changes
mars <env> plan --apply                   # Plan and apply in one step
mars <env> apply                          # Apply changes
mars <env> apply --approve                # Apply without confirmation
mars <env> plan --target=<resource>       # Target specific resource
mars <env> terraform output <name>        # Get terraform output

# Ansible operations
mars <env> ansible-playbook playbook.yaml
mars <env> ansible-playbook -vvvv --aws-sm-secret-id="<id>" --aws-region="<region>" playbook.yaml
mars <env> ansible-vault-encrypt --aws-sm-secret-id="<id>" --aws-region="<region>"
mars <env> ansible-vault-decrypt --aws-sm-secret-id="<id>" --aws-region="<region>"

# Development mode (mount local mars source)
MARS_DEV=true MARS_DEV_ROOT=~/path/to/mars mars <env> <command>

# Debug mode
MARS_DEBUG=true mars <env> <command>

# Interactive shell in container
MARS_SHELL=true mars <env>
```

## Architecture

### Entry Points (`scripts/`)
- `run.sh` - Container entrypoint, sets up user permissions
- `mars.py` - Command router: dispatches to terraform/ansible/packer/alb modules based on command prefix
- `terraform.py` - Terraform wrapper with workspace management, var file aggregation from `vars/common/` and `vars/<env>/`
- `luther_ansible.py` - Ansible wrapper with vault integration (Azure KeyVault, AWS Secrets Manager)

### Ansible Roles (`ansible-roles/`)

**Kubernetes/Helm:**
- `helm`, `helm_charts` - Helm installation and chart deployment
- `kubectl`, `eks_cluster_init`, `eks_upgrade` - Kubernetes/EKS management
- `k8s_static_resources`, `k8s_pv_data`, `k8s_pvc` - Static manifests and storage
- `k8s_external_dns`, `k8s_coredns_config` - DNS configuration

**Hyperledger Fabric (blockchain):**
- `k8s_fabric_ca` - Certificate Authority
- `k8s_fabric_peer`, `k8s_fabric_orderer` - Peer and orderer nodes
- `k8s_fabric_channel`, `k8s_fabric_chaincode` - Channel and chaincode management
- `k8s_fabric_cli`, `k8s_fabric_scripts` - CLI tools and utilities

**Monitoring:**
- `prometheus`, `fluentbit`, `journald` - Metrics and logging

**Infrastructure:**
- `alb_ingress_controller`, `aws_lb_controller` - AWS load balancing
- `bastion_init` - Bastion host setup

### Ansible Plugins (`ansible-plugins/`)
- `filter/dict_filters.py` - `dict_without_keys` filter
- `filter/fabric_filters.py` - `luther_fabric_org_domain` for Fabric domains
- `lookup/grafana_dashboard_dir.py` - Load Grafana dashboards from directory

### Terraform Integration
- Uses tfenv for version management (requires `.terraform-version` file)
- Variable files loaded from `vars/common/*.tfvars` and `vars/<env>/*.tfvars`
- Workspaces correspond to environments

## Version Pinning

In managed repositories:
```bash
# Pin mars version
echo v0.92.0 > .mars-version

# Pin terraform version (required)
echo 1.7.3 > .terraform-version
```

## CI/CD & Releases

- `.github/workflows/mars-ci.yml` - Builds on PRs to main (amd64 + arm64)
- `.github/workflows/mars-release.yml` - Triggered by git tags, builds and pushes multi-arch images to DockerHub
- Images are built with Docker buildx for both amd64 and arm64 architectures
- To release: create a git tag (e.g., `v0.92.0`) and push it - GitHub Actions handles the rest
