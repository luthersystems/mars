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

The image sets `TF_PLUGIN_CACHE_DIR=/opt/tf-plugin-cache` and ships pre-warmed
binaries for the providers pinned by `insideout-terraform-presets`:

- `hashicorp/aws`
- `hashicorp/google`
- `hashicorp/google-beta`

`terraform init` will symlink these into `.terraform/providers/` instead of
downloading them, which both speeds up `init` and keeps Argo workflow artifact
tarballs small (symlinks archive as a few bytes instead of ~1 GB of provider
binary).

**Lockfile requirement.** Terraform validates cache hits against the `h1:`
hashes in `.terraform.lock.hcl`. By default a lockfile only carries the hash
for the platform `terraform init` was run on, so a Mac-generated lockfile will
miss the Linux cache. To get cache hits in both the amd64 and arm64 mars
images, regenerate consumer lockfiles with both Linux platforms:

```
mars <env> terraform -- providers lock \
  -platform=linux_amd64 -platform=linux_arm64
```

If the lockfile is missing the right hash, terraform falls back to downloading
the provider from the registry — same behavior as before this cache existed.

Provider versions are pinned at build time via `AWS_PROVIDER_VERSION` and
`GOOGLE_PROVIDER_VERSION` ARGs in the Dockerfile. Bump them in lockstep with
`insideout-terraform-presets`.

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
