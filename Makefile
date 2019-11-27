include common.mk
STATIC_IMAGE=luthersystems/${PROJECT}

PYTHON_SOURCES=$(shell find . -name '*.py')

# NOTE: There is a bug with PACKER_VERSION=1.4.0 that prevents running in debug mode
PACKER_VERSION=1.3.5
PACKER_ARCHIVE=build/packer_${PACKER_VERSION}_linux_amd64.zip
PACKER=build/packer
PACKER_URL=https://releases.hashicorp.com/packer/${PACKER_VERSION}/$(notdir ${PACKER_ARCHIVE})

# The tfenv project is checked out using a git repository to avoid issues
# because a user didn't set up SSH keys with github.
# Use a luther clone of this repo for reproducibility.
TFENV=build/tfenv
TFENV_REPO=git@bitbucket.org:luthersystems/tfenv.git

# TODO:  Not using keybase for now to avoid installing GUI stuff, fuse, etc
# inside the docker image.  Look for a way to install some core binary without
# dependencies on that other stuff.
KEYBASE=build/keybase/keybase_amd64.deb
KEYBASE_URL=https://prerelease.keybase.io/keybase_amd64.deb

STATIC_IMAGE_DUMMY=${call IMAGE_DUMMY,${STATIC_IMAGE}/${VERSION}}
ECR_STATIC_IMAGE=${ECR_HOST}/${STATIC_IMAGE}

ANSIBLE_ROLES=$(shell find ansible-roles)
ANSIBLE_PLUGINS=$(shell find ansible-plugins)
GRAFANA_DASHBOARDS=$(shell find grafana-dashboards)

.PHONY: default
default: docker-static
	@

.PHONY: docker-static
docker-static: ${STATIC_IMAGE_DUMMY}
	@

.PHONY: docker-push
docker-push: aws-ecr-login ${ECR_STATIC_IMAGE}
	@

.PHONY: clean
clean:
	rm -rf build

${TFENV}:
	mkdir -p $(dir ${TFENV})
	git clone ${TFENV_REPO} ${TFENV}

.PHONY: packer
packer: ${PACKER}

${PACKER}: ${PACKER_ARCHIVE}
	cd $(dir $<) && unzip -o $(notdir $<)
	touch $@

${PACKER_ARCHIVE}:
	wget -O $@ ${PACKER_URL}

.PHONY: aws-ecr-login
aws-ecr-login:
	$(shell aws ecr get-login --region ${AWS_REGION} --no-include-email)

${STATIC_IMAGE_DUMMY}: Dockerfile ${TFENV} ${PACKER} ${PYTHON_SOURCES} ${ANSIBLE_ROLES} ${ANSIBLE_PLUGINS} ${GRAFANA_DASHBOARDS} run.sh ssh_config requirements.txt
	${DOCKER} build \
		-t ${STATIC_IMAGE}:latest \
		-t ${STATIC_IMAGE}:${VERSION} \
		-t ${ECR_STATIC_IMAGE}:latest \
		-t ${ECR_STATIC_IMAGE}:${VERSION} \
		.
	${MKDIR_P} $(dir $@)
	${TOUCH} $@

.PHONY: ${ECR_STATIC_IMAGE}
${ECR_STATIC_IMAGE}: ${STATIC_IMAGE_DUMMY}
	docker push ${ECR_STATIC_IMAGE}:latest
	docker push ${ECR_STATIC_IMAGE}:${VERSION}
