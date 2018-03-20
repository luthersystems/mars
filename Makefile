IMAGE_NAME=luthersystems/mars

# The tfenv project is checked out using a git repository to avoid issues
# because a user didn't set up SSH keys with github.
TFENV=build/tfenv
TFENV_REPO=https://github.com/kamatama41/tfenv.git

# TODO:  Not using keybase for now to avoid installing GUI stuff, fuse, etc
# inside the docker image.  Look for a way to isntall some core binary without
# dependencies on that other stuff.
KEYBASE=build/keybase/keybase_amd64.deb
KEYBASE_URL=https://prerelease.keybase.io/keybase_amd64.deb


.PHONY: default
default: build
	@

.PHONY: build
build: ${TFENV}
	docker build -t ${IMAGE_NAME} .

.PHONY: clean
clean:
	rm -rf build

${TFENV}:
	mkdir -p $(dir ${TFENV})
	git clone ${TFENV_REPO} ${TFENV}
