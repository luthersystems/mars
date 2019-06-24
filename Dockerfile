FROM ubuntu:16.04

ADD build/packer /opt/bin/packer

ADD build/tfenv /opt/tfenv
RUN mkdir -p /opt/tfenv/versions && chmod -R a+w /opt/tfenv/versions
ENV PATH="/opt/tfenv/bin:/opt/bin:${PATH}"

RUN mkdir -p /marsproject /opt/home
ENV HOME="/opt/home"

WORKDIR /marsproject

# Update apt cache and install prerequisites before running tfenv for the first
# time.
#   https://github.com/kamatama41/tfenv/blob/c859abc80bcab1cdb3b166df358e82ff7c1e1d36/README.md#usage
RUN apt-get update && apt-get install -yq git curl unzip perl python3 python3-pip libffi-dev libssl-dev vim

ADD requirements.txt /opt/mars/requirements.txt
RUN pip3 install -r /opt/mars/requirements.txt
RUN mkdir /opt/home/.ansible && chmod a+w /opt/home/.ansible

RUN tfenv install 0.11.2 && \
    tfenv install 0.11.3 && \
    tfenv install 0.11.4

ENTRYPOINT ["/opt/mars/run.sh"]

ADD mars.py /opt/mars/mars.py
ADD command.py /opt/mars/command.py
ADD packer.py /opt/mars/packer.py
ADD luther_ansible.py /opt/mars/luther_ansible.py
ADD terraform.py /opt/mars/terraform.py
RUN chmod a+x /opt/mars/terraform.py
ADD run.sh /opt/mars/run.sh
RUN chmod a+x /opt/mars/run.sh
ADD ssh_config /etc/ssh/ssh_config
# Grab bitbucket.org keys and place in known_hosts
RUN ssh-keyscan -H bitbucket.org >> /etc/ssh/known_hosts

ENV EDITOR="vim -i NONE"
