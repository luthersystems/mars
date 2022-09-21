FROM ubuntu:20.04 as downloader
ARG TARGETARCH
ENV TARGETARCH=$TARGETARCH

RUN apt-get update
RUN apt-get install --no-install-recommends -y gnupg curl ca-certificates
ADD aws-cli-pkg-key.asc /tmp/aws-cli-pkg-key.asc
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

FROM ubuntu:20.04

ADD build/tfenv /opt/tfenv
RUN mkdir -p /opt/tfenv/versions && chmod -R a+w /opt/tfenv/versions
ENV PATH="/opt/tfenv/bin:/opt/bin:${PATH}"

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

ADD requirements.txt /opt/mars/requirements.txt
RUN pip3 install -r /opt/mars/requirements.txt

ADD ansible-reqs.yml /opt/mars/ansible-reqs.yml
RUN ansible-galaxy install -r /opt/mars/ansible-reqs.yml

COPY --from=downloader /tmp/tfedit /opt/bin/tfedit
COPY --from=downloader /tmp/tfmigrate /opt/bin/tfmigrate
COPY --from=downloader /tmp/awscliv2.zip /tmp/awscliv2.zip
RUN unzip -d /tmp /tmp/awscliv2.zip && /tmp/aws/install && rm -rf /tmp/aws*

ENTRYPOINT ["/opt/mars/run.sh"]

ADD scripts /opt/mars/
RUN chmod a+x /opt/mars/run.sh
RUN chmod a+x /opt/mars/terraform.py

ADD ssh_config /etc/ssh/ssh_config
# Grab bitbucket.org keys and place in known_hosts
RUN ssh-keyscan -H bitbucket.org >> /etc/ssh/known_hosts && test -n "$(cat /etc/ssh/known_hosts)"

ADD ansible-roles /opt/ansible/roles
ADD ansible-plugins /opt/ansible/plugins
ENV ANSIBLE_LOOKUP_PLUGINS=/opt/ansible/plugins/lookup
ENV ANSIBLE_FILTER_PLUGINS=/opt/ansible/plugins/filters
ENV ANSIBLE_ROLES_PATH=/opt/ansible/roles

ADD grafana-dashboards /opt/grafana-dashboards
