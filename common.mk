PROJECT=mars
PROJECT_PATH=github.com/luthersystems/${PROJECT}

TAG_SUFFIX ?= -amd64
GIT_TAG ?= $(shell git describe --tags --exact-match 2>/dev/null)
GIT_REVISION ?= $(shell git rev-parse --short HEAD)
VERSION ?= $(if $(strip $(GIT_TAG)),$(GIT_TAG),$(GIT_REVISION))
BUILD_VERSION ?= ${VERSION}${TAG_SUFFIX}

ifeq ($(OS),Windows_NT)
	IS_WINDOWS=1
endif

CP=cp
RM=rm
DOCKER=docker
DOCKER_RUN_OPTS=--rm
DOCKER_RUN=${DOCKER} run ${DOCKER_RUN_OPTS}
CHOWN=$(if $(CIRCLECI),sudo chown,chown)
CHOWN_USR=$(LOGNAME)
CHOWN_USR?=$(USER)
DOCKER_USER=$(shell id -u ${USER}):$(shell id -g ${USER})
CHOWN_GRP=$(if $(or $(IS_WINDOWS),$(CIRCLECI)),,admin)
DOMAKE=$(MAKE) -C $1 $2 # NOTE: this is not used for now as it does not work with -j for some versions of Make
MKDIR_P=mkdir -p
TOUCH=touch
GZIP=gzip
GUNZIP=gunzip
TIME_P=time -p
TAR=tar

# The Makefile determines whether to build a container or not by consulting a
# dummy file that is touched whenever the container is built.  The function,
# IMAGE_DUMMY, computes the path to the dummy file.
DUMMY_TARGET=build/$(1)/$(2)/.dummy
IMAGE_DUMMY=$(call DUMMY_TARGET,image,$(1))
PUSH_DUMMY=$(call DUMMY_TARGET,push,$(1))
MANIFEST_DUMMY=$(call DUMMY_TARGET,manifest,$(1))
FQ_DOCKER_IMAGE ?= docker.io/$(1)

UNAME := $(shell uname)
GIT_LS_FILES=$(shell git ls-files $(1))

DOCKER_WIN_DIR=$(shell cygpath -wm $(realpath $(1)))
DOCKER_NIX_DIR=$(realpath $(1))
DOCKER_DIR=$(if $(IS_WINDOWS),$(call DOCKER_WIN_DIR, $(1)),$(call DOCKER_NIX_DIR, $(1)))

HWTYPE=$(shell uname -m)

.PHONY: echo/%
echo/%:
	@echo "$($*)"
