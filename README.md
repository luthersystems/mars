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

If `$SSH_AUTH_SOCK` is mountable on docker containers (on Linux -- not macOS),
general usage has the following form.

```sh
LOCAL_CACHE="$HOME/.mars/tfenv/versions"
END_USER=$(id -u $USER):$(id -g $USER)
DOCKER_WORKDIR=/terraform

PROJECT_PATH=$(pwd)
docker run --rm -it \
    -u $END_USER \
    -v "$LOCAL_CACHE:/opt/tfenv/versions" \
    -v "$PROJECT_PATH:/terraform" \
    -v "$HOME/.aws/:/opt/home/.aws" \
    -v "$SSH_AUTH_SOCK:$SSH_AUTH_SOCK" \
    -e "SSH_AUTH_SOCK=$SSH_AUTH_SOCK" \
    luthersystems/mars COMMAND [FLAGS] ARGS
```

**NOTE:**  If using macOS then an alternative method for forwarding the host's
ssh-agent to the docker container is necessary (see long discussions spawning
from [this thread](
https://forums.docker.com/t/can-we-re-use-the-osx-ssh-agent-socket-in-a-container/8152)).
It seems like
[uber-common/docker-ssh-forward](https://github.com/uber-common/docker-ssh-agent-forward)
is the defacto best option as the
[avsm/docker-ssh-forward](https://github.com/avsm/docker-ssh-agent-forward)
project recommended from the linked thread seems to be unmaintained now (years
later).


The user (`-u`) is set so that state files in the project's .terraform
directory will be owned by the correct user (not root).  AWS credentials must
be mounted into /opt/home, which is containers value for environment variable
`HOME`.  Finally, in order for ssh to work the ssh-agent's socket must be
mounted into the container from the host and variable `SSH_AUTH_SOCK` has to be
set, telling the container where to find the unix socket.

Mounting to path /opt/tfenv/versions is not a requirement but will prevent
containers run with `--rm` from continuously needing to download versions of
terraform not included in the default installation.  The mount, or
alternatively running with `--rm`, will bypass this issue.  For now, `--rm` and
mounting the cache in the docker command fits our workflow better so we
typically do that.

To ease working with the container it is advisable to either write a wrapper
script orinstall a symlink pointing to a shell script in this project to make
running the container less painful.

On Linux, the following command will install the proper script symlink.

```
ln -s $(pwd)/mars.sh ~/bin/mars
mars -h
```

Users of macOS should instead symlink the macOS specific script `mars_macos.sh`
which will make use of `pinata-ssh-mount` to forward the host ssh agent (see
note above).

```
ln -s $(pwd)/mars_macos.sh ~/bin/mars
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
