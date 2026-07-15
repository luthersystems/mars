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

# Provider versions are kept in sync with insideout-terraform-presets and the
# sandbox-infrastructure-template .terraform.lock.hcl files that run inside this
# image. /etc/terraformrc below configures a filesystem_mirror, but it only
# takes the fast symlink path when the baked version EXACTLY matches the
# consumer's lockfile pin; any unbaked version falls back to a slow direct
# registry download (see the etc/terraformrc comments) — which does not break
# init, but on a slow/throttled egress path *can* time out the ~300MB AWS
# provider download ("could not query provider registry ... Client.Timeout
# exceeded while awaiting headers") and break the deploy.
#
# That is exactly what happened (reliable#2141 e2e): a single baked AWS 6.46.0
# matched NONE of the sandbox stage locks (5.48.0 / 5.100.0 / 6.15.0), so every
# stage fell back to the registry and cloud-provision's hashicorp/aws fetch
# timed out 100% of the time.
#
# sandbox-infrastructure-template#149 then converged every stage onto a single
# modern AWS (6.52.0) and Google (6.10.0), so the mirror only needs one version
# of each. Those exact pins are bounded by the community modules the composed
# stacks pull: terraform-aws-modules/eks 21.x floors aws >= 6.52, and
# terraform-google-modules cap google < 7. Bake exactly those; any future stage
# bump must update this block and re-check against the sandbox locks.
#
# Sources of truth — keep aligned with these lockfile pins (a CI drift guard,
# internal/providercache/providercache_test.go, fails if they diverge):
#   AWS    6.52.0  → sandbox tf/{account-provision,account-setup,cloud-provision,vm-provision,k8s-provision,custom-stack-provision}
#   Google 6.10.0  → sandbox tf/{cloud-provision,custom-stack-provision}
ARG AWS_PROVIDER_VERSIONS="6.52.0"
ARG GOOGLE_PROVIDER_VERSIONS="6.10.0"

RUN mkdir -p ${TF_PLUGIN_CACHE_DIR} /tmp/warmup
# One `terraform init` per (provider, version) into the shared plugin cache;
# the cache accumulates every version so the runtime filesystem_mirror resolves
# every stage's pin via symlink instead of a registry download.
RUN set -eux; \
  for v in ${AWS_PROVIDER_VERSIONS}; do \
    d="/tmp/warmup/aws-$v"; mkdir -p "$d"; \
    printf '%s\n' \
      'terraform {' \
      '  required_providers {' \
      "    aws = { source = \"hashicorp/aws\", version = \"= $v\" }" \
      '  }' \
      '}' > "$d/main.tf"; \
    ( cd "$d" && terraform init -input=false -backend=false ); \
  done; \
  for v in ${GOOGLE_PROVIDER_VERSIONS}; do \
    d="/tmp/warmup/google-$v"; mkdir -p "$d"; \
    printf '%s\n' \
      'terraform {' \
      '  required_providers {' \
      "    google      = { source = \"hashicorp/google\",      version = \"= $v\" }" \
      "    google-beta = { source = \"hashicorp/google-beta\", version = \"= $v\" }" \
      '  }' \
      '}' > "$d/main.tf"; \
    ( cd "$d" && terraform init -input=false -backend=false ); \
  done
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
  CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/insideout-preflight ./cmd/insideout-preflight && \
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
  util-linux \
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
COPY --from=mars-cli /out/insideout-preflight /opt/bin/insideout-preflight
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
RUN chmod a+x /opt/bin/insideout-preflight && ln -sf /opt/bin/insideout-preflight /usr/local/bin/insideout-preflight

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
RUN mv /opt/tfenv/bin/terraform /opt/tfenv/bin/terraform.tfenv-original
COPY scripts/tfenv-terraform-lock-wrapper.sh /opt/tfenv/bin/terraform
RUN chmod a+x /opt/tfenv/bin/terraform /opt/tfenv/bin/terraform.tfenv-original
ENV PATH="/opt/tfenv/bin:/opt/bin:${PATH}"

# Pre-install the terraform versions commonly requested by consumers via
# `.terraform-version`. Without this, each Argo pod's first `tfenv install`
# round-trips to releases.hashicorp.com (download tarball + SHA + GPG verify
# + unzip) — observed at ~30s per pod, which is pure dead time when the
# version doesn't change.
#
# Default tracks TF_WARMUP_VERSION (1.9.8) — the same terraform used to warm
# the plugin cache in the tf-providers stage. Consumers pinned to a different
# version still work: tfenv falls back to downloading at runtime under the
# Mars wrapper's flock-guarded install path. This is a fast-path, not a hard
# pin.
ARG TFENV_PREINSTALL_VERSIONS="1.9.8"
RUN for v in ${TFENV_PREINSTALL_VERSIONS}; do \
    /opt/tfenv/bin/tfenv install "$v" \
    && chmod -R a+rX /opt/tfenv/versions/$v ; \
  done

# Pre-warmed terraform provider cache for the providers pinned by
# insideout-terraform-presets / sandbox-infrastructure-template.
COPY --from=tf-providers /opt/tf-plugin-cache /opt/tf-plugin-cache

# Tell terraform to install the baked providers via a filesystem mirror rather
# than the default TF_PLUGIN_CACHE_DIR path. With a filesystem_mirror, init
# *symlinks* the per-arch provider directory into <workdir>/.terraform/
# providers/ instead of copying its contents. That keeps the per-arch provider
# binaries (~200 MB) out of Argo's tar of /marsproject — see
# luthersystems/mars#168. include is a per-provider list (not a wildcard) and
# the direct{} block is permissive so any other hashicorp/* provider, or a
# version of these providers that drifts from the bake, still resolves via
# direct registry download.
COPY etc/terraformrc /etc/terraformrc
RUN chmod a+r /etc/terraformrc
ENV TF_CLI_CONFIG_FILE=/etc/terraformrc
# TF_PLUGIN_CACHE_DIR is intentionally NOT set here: with the filesystem_mirror
# block in /etc/terraformrc, the cache_dir behavior is redundant and, on older
# terraform versions, would re-introduce the copy-instead-of-symlink path for
# providers that fall outside the mirror's include list.
#
# Belt-and-suspenders for any provider/version that still falls through to the
# registry `direct{}` path (drift, or a hashicorp/* provider outside the mirror
# include list): raise the registry client timeout from its 10s default so a
# slow-but-progressing query over a cold customer-account NAT doesn't cancel
# "while awaiting headers" and break init (reliable#2141). The mirror is the
# real fix; this just stops a transient slow path from being fatal.
ENV TF_REGISTRY_CLIENT_TIMEOUT=30

COPY --from=downloader /usr/local/bin/helm /opt/bin/helm

ARG HELM_DIFF_VERSION
ENV HELM_DIFF_VERSION=$HELM_DIFF_VERSION

RUN helm plugin install https://github.com/databus23/helm-diff --version ${HELM_DIFF_VERSION} --verify=false
