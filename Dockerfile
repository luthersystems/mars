FROM ubuntu:22.04 as downloader
ARG TARGETARCH
ENV TARGETARCH=$TARGETARCH

RUN apt-get update -y && apt-get install --no-install-recommends -yq gnupg curl ca-certificates git
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
RUN curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/${HELM_VERSION}/scripts/get-helm-3 && \
  chmod 700 get_helm.sh && \
  ./get_helm.sh

FROM ubuntu:22.04

RUN mkdir -p /marsproject /opt/home
ENV HOME="/opt/home"

WORKDIR /marsproject

# https://githubmemory.com/repo/pypa/pip/issues/10219
# Because this is an ubuntu container and not centos C.UTF-8 is the correct fix
# and not en_us.UTF-8
ENV LANG=C.UTF-8 LC_ALL=C.UTF-8 PYTHONIOENCODING=UTF-8 DEBIAN_FRONTEND=noninteractive

# Update apt cache and install prerequisites before running tfenv for the first
# time.
#   https://github.com/kamatama41/tfenv/blob/c859abc80bcab1cdb3b166df358e82ff7c1e1d36/README.md#usage

RUN apt-get update -y && apt-get install --no-install-recommends -yq \
  apt-transport-https \
  build-essential \
  ca-certificates \
  curl \
  git \
  gnupg \
  jq \
  libffi-dev \
  libssl-dev \
  lsb-release \
  openssh-client \
  perl \
  python3 \
  python3-dev \
  python3-pip \
  rsync \
  software-properties-common \
  unzip

RUN curl -fsSL https://apt.releases.hashicorp.com/gpg | apt-key add - && \
  apt-add-repository "deb [arch=$(dpkg --print-architecture)] https://apt.releases.hashicorp.com $(lsb_release -cs) main" && \
  apt update -y && apt install -y packer

RUN pip3 install --upgrade pip

COPY requirements.txt /opt/mars/requirements.txt
RUN pip3 install --no-cache-dir -r /opt/mars/requirements.txt

COPY ansible-reqs.yml /opt/mars/ansible-reqs.yml
RUN ansible-galaxy install -r /opt/mars/ansible-reqs.yml

COPY --from=downloader /tmp/tfedit /opt/bin/tfedit
COPY --from=downloader /tmp/tfmigrate /opt/bin/tfmigrate
COPY --from=downloader /tmp/awscliv2.zip /tmp/awscliv2.zip
RUN unzip -d /tmp /tmp/awscliv2.zip && /tmp/aws/install && rm -rf /tmp/aws*

ENTRYPOINT ["/opt/mars/run.sh"]

COPY scripts /opt/mars/
RUN chmod a+x /opt/mars/run.sh /opt/mars/terraform.py

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

COPY --from=downloader /usr/local/bin/helm /opt/bin/helm

RUN curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.28/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
RUN echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.28/deb/ /" | tee /etc/apt/sources.list.d/kubernetes.list
RUN apt-get update && apt-get install -y kubectl
