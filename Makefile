include common.mk
STATIC_IMAGE=luthersystems/${PROJECT}

SCRIPTS=$(shell find scripts -type f)

AWSCLI_VERSION=2.7.0

TFEDIT_VERSION=0.0.3
TFMIGRATE_VERSION=0.3.3
TFENV_VER=v3.0.0

STATIC_IMAGE_DUMMY=${call IMAGE_DUMMY,${STATIC_IMAGE}/${VERSION}}
FQ_STATIC_IMAGE=$(call FQ_DOCKER_IMAGE,${PUBLIC_STATIC_IMAGE})
FQ_STATIC_IMAGE_DUMMY=$(call PUSH_DUMMY,${FQ_STATIC_IMAGE}/${BUILD_VERSION})
FQ_MANIFEST_DUMMY=$(call MANIFEST_DUMMY,${FQ_STATIC_IMAGE}/${BUILD_VERSION})

ANSIBLE_ROLES=$(shell find ansible-roles)
ANSIBLE_PLUGINS=$(shell find ansible-plugins)
GRAFANA_DASHBOARDS=$(shell find grafana-dashboards)

LOCALARCH=$(if $(findstring ${HWTYPE},"x86_64"),amd64,${HWTYPE})

.PHONY: default
default: static
	@

.PHONY: static
static: ${STATIC_IMAGE_DUMMY}
	@

.PHONY: push
push: ${FQ_STATIC_IMAGE_DUMMY}
	@

.PHONY: clean
clean:
	rm -rf build

build-%: LOADARG=$(if $(findstring $*,${LOCALARCH}),--load)
build-%: Dockerfile  ${ANSIBLE_ROLES} ${ANSIBLE_PLUGINS} ${GRAFANA_DASHBOARDS} ${SCRIPTS} ssh_config requirements.txt
	${DOCKER} buildx build \
		--platform linux/$* \
		--build-arg AWSCLI_VER=${AWSCLI_VERSION} \
		--build-arg TFEDIT_VER=${TFEDIT_VERSION} \
		--build-arg TFMIGRATE_VER=${TFMIGRATE_VERSION} \
		--build-arg TFENV_VER=${TFENV_VER} \
		${LOADARG} \
		-t ${STATIC_IMAGE}:${VERSION} \
		.

${STATIC_IMAGE_DUMMY}: 
	make build-${LOCALARCH}
	${MKDIR_P} $(dir $@)
	${TOUCH} $@

${FQ_STATIC_IMAGE_DUMMY}: ${STATIC_IMAGE_DUMMY}
	${DOCKER} tag ${STATIC_IMAGE}:${VERSION} ${FQ_STATIC_IMAGE}:${BUILD_VERSION}
	${DOCKER} push ${FQ_STATIC_IMAGE}:${BUILD_VERSION}
	${MKDIR_P} $(dir $@)
	${TOUCH} $@

.PHONY: push-manifests
push-manifests: ${FQ_MANIFEST_DUMMY}
	@

${FQ_MANIFEST_DUMMY}:
	${DOCKER} buildx imagetools create \
		--tag ${FQ_STATIC_IMAGE}:latest \
		${FQ_STATIC_IMAGE}:${VERSION}-arm64 \
		${FQ_STATIC_IMAGE}:${VERSION}-amd64
	${DOCKER} buildx imagetools create \
		--tag ${FQ_STATIC_IMAGE}:${VERSION} \
		${FQ_STATIC_IMAGE}:${VERSION}-arm64 \
		${FQ_STATIC_IMAGE}:${VERSION}-amd64
