#Mars

Luther Systems infrastructure management tool.

#Setup

Build the container

```
make
```

Make sure ssh-agent has your key private key

```
ssh-add -l
```

If ssh-add doesn't print anything, it doesn't have you key.  Run `ssh-add`
without arguments or point it to the appropriate key file in special cases.

#Usage

Ensure your project has a `.terraform-version` file specifying which terraform
version to use (technically optional).  See
[tfenv](https://github.com/kamatama41/tfenv) for more details.

```
echo 0.11.3 > .terraform-version
```

General usage has the following form.

```sh
END_USER=$(id -u $USER):$(id -g $USER)
DOCKER_WORKDIR=/terraform

PROJECT_PATH=$(pwd)
docker run --rm -it \
    -u $END_USER \
    -v "$PROJECT_PATH:/terraform" \
    -v "$HOME/.aws/:/opt/home/.aws" \
    -v "$SSH_AUTH_SOCK:$SSH_AUTH_SOCK" \
    -e "SSH_AUTH_SOCK=$SSH_AUTH_SOCK" \
    luthersystems/mars COMMAND [FLAGS] ARGS
```

The user (`-u`) is set so that state files in the project's .terraform
directory will be owned by the correct user (not root).  AWS credentials must
be mounted into /opt/home, which is containers value for environment variable
`HOME`.  Finally, in order for ssh to work the ssh-agent's socket must be
mounted into the container from the host and variable `SSH_AUTH_SOCK` has to be
set, telling the container where to find the unix socket..

You can install a symlink pointing to "mars.sh" to make running the container
less painful.

```
ln -s $(pwd)/mars.sh ~/bin/mars
mars -h
```

If you need to run a raw terraform command using the `terraform` binary
installed in the container you may run `mars terraform` but without care python
will intercept options/flags intended for terraform.  Often you will have to
use the special argument `--` to tell python not to try and parse the flags
meant for terraform.

```
mars dev terraform -- providers --help
```
