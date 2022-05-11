FROM ubuntu:20.04

ARG TARGETARCH
ENV TARGETARCH=$TARGETARCH

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
RUN apt-get update -y && apt-get install -yq git curl unzip jq perl python3 python3-pip libffi-dev libssl-dev vim ca-certificates apt-transport-https lsb-release gnupg rsync software-properties-common

RUN curl -fsSL https://apt.releases.hashicorp.com/gpg | apt-key add - && \
    apt-add-repository "deb [arch=$(dpkg --print-architecture)] https://apt.releases.hashicorp.com $(lsb_release -cs) main" && \
    apt update -y && apt install -y packer

RUN pip3 install --upgrade pip

ADD requirements.txt /opt/mars/requirements.txt
RUN pip3 install -r /opt/mars/requirements.txt

RUN pip install azure-cli --upgrade

RUN pip install https://github.com/boto/botocore/archive/v2.zip https://github.com/aws/aws-cli/archive/v2.zip
RUN apt-get -yq --no-install-recommends install groff # required by awscli

ADD ansible-reqs.yml /opt/mars/ansible-reqs.yml
RUN ansible-galaxy install -r /opt/mars/ansible-reqs.yml

RUN tfenv install 0.12.31

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

ENV EDITOR="vim -i NONE"
