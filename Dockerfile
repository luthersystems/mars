# syntax=docker/dockerfile:1.7

FROM ubuntu:24.04 AS downloader
ARG TARGETARCH
ENV TARGETARCH=$TARGETARCH

RUN apt update -y
RUN apt install --no-install-recommends -yq \
  apt-transport-https \
  lsb-release \
  gnupg \
  gnupg \
  curl \
  ca-certificates \
  git

RUN mkdir -p /tmp/keyrings /tmp/sources.list.d
RUN curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.28/deb/Release.key | gpg --dearmor -o /tmp/keyrings/kubernetes-apt-keyring.gpg
RUN echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.28/deb/ /" | tee /tmp/sources.list.d/kubernetes.list
RUN curl -fsSL https://apt.releases.hashicorp.com/gpg | gpg --dearmor -o /tmp/keyrings/hashicorp-apt-keyring.gpg
RUN echo "deb [signed-by=/etc/apt/keyrings/hashicorp-apt-keyring.gpg] https://apt.releases.hashicorp.com $(lsb_release -cs) main" | tee /tmp/sources.list.d/hashicorp.list
RUN curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /tmp/keyrings/google-cloud-apt-keyring.gpg
RUN echo "deb [signed-by=/etc/apt/keyrings/google-cloud-apt-keyring.gpg] https://packages.cloud.google.com/apt cloud-sdk main" | tee /tmp/sources.list.d/google-cloud-sdk.list

COPY aws-cli-pkg-key.asc /tmp/aws-cli-pkg-key.asc
RUN gpg --import /tmp/aws-cli-pkg-key.asc

WORKDIR /tmp

ARG AWSCLI_VER
ENV AWSCLI_VER=$AWSCLI_VER
RUN curl -fsSL -o awscliv2.zip "https://awscli.amazonaws.com/awscli-exe-linux-$(uname -m)-${AWSCLI_VER}.zip"
RUN curl -fsSL -o awscliv2.sig "https://awscli.amazonaws.com/awscli-exe-linux-$(uname -m)-${AWSCLI_VER}.zip.sig"
RUN gpg --verify awscliv2.sig awscliv2.zip

COPY "SHA256SUMS.${TARGETARCH}" /tmp/SHA256SUMS

ARG TFEDIT_VER
ENV TFEDIT_VER=$TFEDIT_VER
ARG TFEDIT_CHECKSUM
ENV TFEDIT_CHECKSUM=$TFEDIT_CHECKSUM
RUN curl -fsSL -o tfedit.tar.gz https://github.com/minamijoyo/tfedit/releases/download/v${TFEDIT_VER}/tfedit_${TFEDIT_VER}_linux_${TARGETARCH}.tar.gz

ARG TFMIGRATE_VER
ENV TFMIGRATE_VER=$TFMIGRATE_VER
ARG TFMIGRATE_CHECKSUM
ENV TFMIGRATE_CHECKSUM=$TFMIGRATE_CHECKSUM
RUN curl -fsSL -o tfmigrate.tar.gz https://github.com/minamijoyo/tfmigrate/releases/download/v${TFMIGRATE_VER}/tfmigrate_${TFMIGRATE_VER}_linux_${TARGETARCH}.tar.gz

RUN sha256sum --check --status --strict SHA256SUMS
RUN tar -xvf tfedit.tar.gz tfedit
RUN tar -xvf tfmigrate.tar.gz tfmigrate

ARG TFENV_VER
ENV TFENV_VER=$TFENV_VER
RUN git clone -b $TFENV_VER --depth 1 https://github.com/tfutils/tfenv.git /tmp/tfenv

ARG HELM_VERSION
ENV HELM_VERSION=$HELM_VERSION

RUN curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/${HELM_VERSION}/scripts/get-helm-4 && \
  chmod 700 get_helm.sh && \
  ./get_helm.sh --version ${HELM_VERSION}

FROM ubuntu:24.04 AS venv

RUN apt update -y && apt install --no-install-recommends -yq \
  build-essential \
  ca-certificates \
  libffi-dev \
  libssl-dev \
  python3-dev \
  python3-venv

COPY requirements.txt /tmp/requirements.txt
RUN mkdir -p /opt/mars
RUN python3 -m venv /opt/mars_venv
RUN /opt/mars_venv/bin/pip install setuptools
RUN /opt/mars_venv/bin/pip install -r /tmp/requirements.txt

FROM ubuntu:24.04 AS tf-providers
ARG TARGETARCH
ENV TF_PLUGIN_CACHE_DIR=/opt/tf-plugin-cache

RUN apt update -y && apt install --no-install-recommends -yq \
  ca-certificates \
  curl \
  unzip

# Throwaway terraform used only to populate the plugin cache. Not copied into
# the final image; consumers still pick their TF version via tfenv.
ARG TF_WARMUP_VERSION=1.9.8
RUN cd /tmp \
  && curl -fsSL -o "terraform_${TF_WARMUP_VERSION}_linux_${TARGETARCH}.zip" "https://releases.hashicorp.com/terraform/${TF_WARMUP_VERSION}/terraform_${TF_WARMUP_VERSION}_linux_${TARGETARCH}.zip" \
  && curl -fsSL -o tf_SHA256SUMS "https://releases.hashicorp.com/terraform/${TF_WARMUP_VERSION}/terraform_${TF_WARMUP_VERSION}_SHA256SUMS" \
  && grep "  terraform_${TF_WARMUP_VERSION}_linux_${TARGETARCH}.zip$" tf_SHA256SUMS | sha256sum -c --strict - \
  && unzip -d /usr/local/bin "terraform_${TF_WARMUP_VERSION}_linux_${TARGETARCH}.zip" \
  && rm "terraform_${TF_WARMUP_VERSION}_linux_${TARGETARCH}.zip" tf_SHA256SUMS

# Provider versions are kept in sync with insideout-terraform-presets.
# Bump these together with the presets repo to keep cache hits useful.
ARG AWS_PROVIDER_VERSION=6.45.0
ARG GOOGLE_PROVIDER_VERSION=6.10.0

RUN mkdir -p ${TF_PLUGIN_CACHE_DIR} /tmp/warmup
WORKDIR /tmp/warmup
RUN printf '%s\n' \
  'terraform {' \
  '  required_providers {' \
  "    aws         = { source = \"hashicorp/aws\",         version = \"= ${AWS_PROVIDER_VERSION}\" }" \
  "    google      = { source = \"hashicorp/google\",      version = \"= ${GOOGLE_PROVIDER_VERSION}\" }" \
  "    google-beta = { source = \"hashicorp/google-beta\", version = \"= ${GOOGLE_PROVIDER_VERSION}\" }" \
  '  }' \
  '}' \
  > main.tf
RUN terraform init -input=false -backend=false
# Ensure runtime users can read the cache even when Docker copies as root.
RUN chmod -R a+rX ${TF_PLUGIN_CACHE_DIR}

FROM golang:1.25-bookworm AS mars-cli
ARG TARGETARCH
ARG MARS_VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN mkdir -p /out && \
  CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -X github.com/luthersystems/mars/internal/cli.Version=${MARS_VERSION}" -o /out/mars ./cmd/mars && \
  CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/mars-entrypoint ./cmd/mars-entrypoint && \
  CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/insideout-reverse-import ./cmd/insideout-reverse-import && \
  CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/vault-aws-secretsmanager ./cmd/vault-aws-secretsmanager && \
  CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/vault-az-keyvault ./cmd/vault-az-keyvault

FROM ubuntu:24.04

RUN apt update -y && apt install --no-install-recommends -yq \
  ca-certificates \
  curl \
  git \
  jq \
  openssh-client \
  perl \
  python3 \
  rsync \
  unzip \
  gnupg

COPY --from=downloader /tmp/keyrings /etc/apt/keyrings
COPY --from=downloader /tmp/sources.list.d /etc/apt/sources.list.d
RUN apt update -y && apt install --no-install-recommends -yq \
  kubectl \
  packer \
  google-cloud-cli

COPY --from=venv /opt/mars_venv /opt/mars_venv
ENV PATH="/opt/mars_venv/bin:${PATH}"

RUN mkdir -p /marsproject /opt/home
ENV HOME="/opt/home"

WORKDIR /marsproject

COPY ansible-reqs.yml /opt/mars/ansible-reqs.yml
RUN ansible-galaxy install -r /opt/mars/ansible-reqs.yml

COPY --from=downloader /tmp/tfedit /opt/bin/tfedit
COPY --from=downloader /tmp/tfmigrate /opt/bin/tfmigrate
COPY --from=mars-cli /out/mars /opt/bin/mars
COPY --from=mars-cli /out/mars-entrypoint /opt/bin/mars-entrypoint
COPY --from=mars-cli /out/insideout-reverse-import /opt/bin/insideout-reverse-import
COPY --from=mars-cli /out/vault-aws-secretsmanager /opt/bin/vault-aws-secretsmanager
COPY --from=mars-cli /out/vault-az-keyvault /opt/bin/vault-az-keyvault
COPY --from=downloader /tmp/awscliv2.zip /tmp/awscliv2.zip
RUN unzip -d /tmp /tmp/awscliv2.zip && /tmp/aws/install && rm -rf /tmp/aws*

ENTRYPOINT ["/opt/mars/mars-entrypoint"]

RUN chmod a+x /opt/bin/mars /opt/bin/mars-entrypoint /opt/bin/vault-aws-secretsmanager /opt/bin/vault-az-keyvault && \
  ln -sf /opt/bin/mars /opt/mars/mars && \
  ln -sf /opt/bin/mars-entrypoint /opt/mars/mars-entrypoint && \
  ln -sf /opt/bin/vault-aws-secretsmanager /opt/mars/vault-aws-secretsmanager && \
  ln -sf /opt/bin/vault-az-keyvault /opt/mars/vault-az-keyvault
RUN chmod a+x /opt/bin/insideout-reverse-import && ln -sf /opt/bin/insideout-reverse-import /usr/local/bin/insideout-reverse-import

COPY ssh_config /etc/ssh/ssh_config
# Grab bitbucket.org keys and place in known_hosts
RUN ssh-keyscan -H github.com >> /etc/ssh/known_hosts && test -n "$(cat /etc/ssh/known_hosts)"
RUN ssh-keyscan -H bitbucket.org >> /etc/ssh/known_hosts && test -n "$(cat /etc/ssh/known_hosts)"

COPY ansible-roles /opt/ansible/roles
COPY ansible-plugins /opt/ansible/plugins
ENV ANSIBLE_LOOKUP_PLUGINS=/opt/ansible/plugins/lookup
ENV ANSIBLE_FILTER_PLUGINS=/opt/ansible/plugins/filters
ENV ANSIBLE_ROLES_PATH=/opt/ansible/roles

COPY grafana-dashboards /opt/grafana-dashboards

COPY --from=downloader /tmp/tfenv /opt/tfenv
RUN mkdir -p /opt/tfenv/versions && chmod -R a+w /opt/tfenv/versions && echo 'trust-tfenv: yes' > /opt/tfenv/use-gpgv
ENV PATH="/opt/tfenv/bin:/opt/bin:${PATH}"

# Pre-warmed terraform provider cache for the providers pinned by
# insideout-terraform-presets. Consumers get cache hits as long as their
# .terraform.lock.hcl carries linux_amd64 / linux_arm64 h1 hashes; otherwise
# terraform falls back to downloading from the registry as before.
COPY --from=tf-providers /opt/tf-plugin-cache /opt/tf-plugin-cache
ENV TF_PLUGIN_CACHE_DIR=/opt/tf-plugin-cache

COPY --from=downloader /usr/local/bin/helm /opt/bin/helm

ARG HELM_DIFF_VERSION
ENV HELM_DIFF_VERSION=$HELM_DIFF_VERSION

RUN helm plugin install https://github.com/databus23/helm-diff --version ${HELM_DIFF_VERSION} --verify=false
