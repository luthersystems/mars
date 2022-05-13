include common.mk
STATIC_IMAGE=luthersystems/${PROJECT}

SCRIPTS=$(shell find scripts -type f)

# The tfenv project is checked out using a git repository to avoid issues
# because a user didn't set up SSH keys with github.
# Use a luther clone of this repo for reproducibility.
TFENV=build/tfenv
TFENV_REPO=git@bitbucket.org:luthersystems/tfenv.git

STATIC_IMAGE_DUMMY=${call IMAGE_DUMMY,${STATIC_IMAGE}/${VERSION}}
ECR_STATIC_IMAGE=${ECR_HOST}/${STATIC_IMAGE}

ANSIBLE_ROLES=$(shell find ansible-roles)
ANSIBLE_PLUGINS=$(shell find ansible-plugins)
GRAFANA_DASHBOARDS=$(shell find grafana-dashboards)

ECR_LOGIN=bash get-ecr-token.sh ${ECR_HOST}

PLATFORMS=linux/amd64,linux/arm64

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

.PHONY: aws-ecr-login
aws-ecr-login:
	${ECR_LOGIN}

${STATIC_IMAGE_DUMMY}: Dockerfile ${TFENV} ${ANSIBLE_ROLES} ${ANSIBLE_PLUGINS} ${GRAFANA_DASHBOARDS} ${SCRIPTS} ssh_config requirements.txt
	${DOCKER} buildx build \
		--platform ${PLATFORMS} \
		-t ${STATIC_IMAGE}:latest \
		-t ${STATIC_IMAGE}:${VERSION} \
		-t ${ECR_STATIC_IMAGE}:latest \
		-t ${ECR_STATIC_IMAGE}:${VERSION} \
		.
	${MKDIR_P} $(dir $@)
	${TOUCH} $@

.PHONY: ${ECR_STATIC_IMAGE}
${ECR_STATIC_IMAGE}: ${STATIC_IMAGE_DUMMY}
	${DOCKER} buildx build \
		--push \
		--platform ${PLATFORMS} \
		-t ${ECR_STATIC_IMAGE}:latest \
		-t ${ECR_STATIC_IMAGE}:${VERSION} \
		.
