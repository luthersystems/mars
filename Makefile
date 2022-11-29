include common.mk
STATIC_IMAGE=luthersystems/${PROJECT}

SCRIPTS=$(shell find scripts -type f)

AWSCLI_VERSION=2.7.0

TFEDIT_VERSION=0.0.3
TFMIGRATE_VERSION=0.3.3
TFENV_VER=v3.0.0

STATIC_IMAGE_DUMMY=${call IMAGE_DUMMY,${STATIC_IMAGE}/${VERSION}}
ECR_STATIC_IMAGE=${ECR_HOST}/${STATIC_IMAGE}

ANSIBLE_ROLES=$(shell find ansible-roles)
ANSIBLE_PLUGINS=$(shell find ansible-plugins)
GRAFANA_DASHBOARDS=$(shell find grafana-dashboards)

ECR_LOGIN=bash get-ecr-token.sh ${ECR_HOST}

PLATFORMS=linux/amd64,linux/arm64
LOCALARCH=$(if $(findstring ${HWTYPE},"x86_64"),amd64,${HWTYPE})

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

.PHONY: aws-ecr-login
aws-ecr-login:
	${ECR_LOGIN}

build-%: LOADARG=$(if $(findstring $*,${LOCALARCH}),--load)
build-%: Dockerfile  ${ANSIBLE_ROLES} ${ANSIBLE_PLUGINS} ${GRAFANA_DASHBOARDS} ${SCRIPTS} ssh_config requirements.txt
	${DOCKER} buildx build \
		--platform linux/$* \
		--build-arg AWSCLI_VER=${AWSCLI_VERSION} \
		--build-arg TFEDIT_VER=${TFEDIT_VERSION} \
		--build-arg TFMIGRATE_VER=${TFMIGRATE_VERSION} \
		--build-arg TFENV_VER=${TFENV_VER} \
		${LOADARG} \
		-t ${STATIC_IMAGE}:latest \
		-t ${STATIC_IMAGE}:${VERSION} \
		-t ${ECR_STATIC_IMAGE}:latest \
		-t ${ECR_STATIC_IMAGE}:${VERSION} \
		.

${STATIC_IMAGE_DUMMY}: build-amd64 build-arm64
	${MKDIR_P} $(dir $@)
	${TOUCH} $@

.PHONY: ${ECR_STATIC_IMAGE}
${ECR_STATIC_IMAGE}: ${STATIC_IMAGE_DUMMY}
	${DOCKER} buildx build \
		--push \
		--platform ${PLATFORMS} \
		--build-arg AWSCLI_VER=${AWSCLI_VERSION} \
		--build-arg TFEDIT_VER=${TFEDIT_VERSION} \
		--build-arg TFMIGRATE_VER=${TFMIGRATE_VERSION} \
		--build-arg TFENV_VER=${TFENV_VER} \
		-t ${ECR_STATIC_IMAGE}:latest \
		-t ${ECR_STATIC_IMAGE}:${VERSION} \
		.
