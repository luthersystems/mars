# Mars

Luther Systems infrastructure management tool.

# Installation

Clone this repository.  For ease of access, alias the name `mars` to the
`mars_macos.sh` script in this repo.  Add the following snippet to your shell
configuration, substituting the correct path to this repository:

```
alias mars="$HOME/mars/mars_macos.sh"
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
installed in the container you may run `mars terraform` but without care python
will intercept options/flags intended for terraform.  Often you will have to
use the special argument `--` to tell python not to try and parse the flags
meant for terraform.

```
mars dev terraform -- providers --help
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

