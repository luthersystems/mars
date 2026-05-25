# Mars

Luther Systems infrastructure management tool.

# Installation

```
brew install luthersystems/repo/mars
```

## Additional requirements for ansible

If running ansible locally, you will also need ssh agent access through mars.
This is currently accomplished using the `pinata-ssh-forward` tool from
[uber-common/docker-ssh-forward](https://github.com/uber-common/docker-ssh-agent-forward).
Follow the installation instructions there to install the tool and make sure
`pinata-ssh-forward` is in your path.

In addition, make sure ssh-agent has your ssh key loaded:

```
ssh-add -l
```

If ssh-add doesn't print anything, it doesn't have you key.  Run `ssh-add`
without arguments or point it to the appropriate key file in special cases.

## Terraform

If you need to run a raw terraform command using the `terraform` binary
installed in the container you may run `mars terraform`. Use the special
argument `--` before raw Terraform flags so Mars passes them through instead of
parsing them as Mars flags.

```
mars dev terraform -- providers --help
```

### Pre-warmed provider plugin cache

The image ships pre-warmed binaries for the providers pinned by
`insideout-terraform-presets` / `sandbox-infrastructure-template` at
`/opt/tf-plugin-cache`:

- `hashicorp/aws`
- `hashicorp/google`
- `hashicorp/google-beta`

The image's `/etc/terraformrc` (selected via `TF_CLI_CONFIG_FILE`) configures a
`filesystem_mirror` for those three providers. `terraform init` **symlinks**
the per-arch provider binary out of `/opt/tf-plugin-cache` into
`<workdir>/.terraform/providers/...` rather than copying it. That keeps the
per-arch provider binaries (~200 MB) out of Argo workflow artifact tarballs of
`/marsproject` — the receiving pod runs the same mars image and resolves the
symlink against its own `/opt/tf-plugin-cache`.

**Version drift falls back to download.** If a consumer's `.terraform.lock.hcl`
pins a version of one of these providers that isn't in `/opt/tf-plugin-cache`,
terraform falls through to the `direct` installation method and downloads from
the registry — same behavior as before this image's cache existed (just
slower than the mirror path). For maximum cache hits, bump
`AWS_PROVIDER_VERSION` / `GOOGLE_PROVIDER_VERSION` in the Dockerfile in
lockstep with consumer lockfile pins, then cut a new mars tag. All other
hashicorp providers (`random`, `null`, `time`, `tls`, ...) always resolve via
direct registry download as before.

**Lockfile platform coverage.** A lockfile generated on macOS only carries
`darwin_*` h1 hashes by default and will fail to validate the Linux cache hit.
Regenerate consumer lockfiles with both Linux platforms so amd64 and arm64
mars images can use the mirror:

```
mars <env> terraform -- providers lock \
  -platform=linux_amd64 -platform=linux_arm64
```

# Setting up managed repositories

Place a .mars-version file at the root of the mars managed infrastructure
repository to pin the version of mars for the repository. For example, to set
the mars version to v0.81.1, run the following from the target repo.

```
echo v0.82.1 > $(git rev-parse --show-toplevel)/.mars-version
```

Ensure all terraform projects have a `.terraform-version` file specifying which
terraform version to use.  See [tfenv](https://github.com/kamatama41/tfenv) for
more details.

```
echo 1.7.3 > .terraform-version
```

# Building

To build the container, run:

```
make
```

Architecture-specific builds are available with:

```
make build-amd64
make build-arm64
```

Tagged releases build and push both architectures, then publish Docker
manifests with `make push-manifests`.

The Mars command dispatcher, container entrypoint, and Ansible vault-id helpers
are built from Go during the Docker build and copied into `/opt/mars/`. The
container still includes Python because Ansible and Azure CLI depend on it.

For Go CLI development with `MARS_DEV=true`, set `MARS_DEV_BINARY` to a
Linux-compatible `mars` binary if you want to override the binary in the image.
`MARS_DEV_ENTRYPOINT`, `MARS_DEV_VAULT_AWS`, and `MARS_DEV_VAULT_AZ` can
override the other Go helper binaries.
