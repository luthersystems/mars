FROM ubuntu:24.04 as downloader
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

FROM ubuntu:24.04 as venv

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
RUN /opt/mars_venv/bin/pip install -r /tmp/requirements.txt

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
  unzip

COPY --from=downloader /tmp/keyrings /etc/apt/keyrings
COPY --from=downloader /tmp/sources.list.d /etc/apt/sources.list.d
RUN apt update -y && apt install --no-install-recommends -yq \
  kubectl \
  packer

COPY --from=venv /opt/mars_venv /opt/mars_venv
ENV PATH="/opt/mars_venv/bin:${PATH}"

RUN mkdir -p /marsproject /opt/home
ENV HOME="/opt/home"

WORKDIR /marsproject

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

