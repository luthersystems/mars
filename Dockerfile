FROM ubuntu:16.04

ADD build/tfenv /opt/tfenv
RUN mkdir -p /opt/tfenv/versions && chmod -R a+w /opt/tfenv/versions
ENV PATH="/opt/tfenv/bin:${PATH}"

RUN mkdir -p /terraform /opt/home
ENV HOME="/opt/home"

WORKDIR /terraform

# Update apt cache and install prerequisites before running tfenv for the first
# time.
#   https://github.com/kamatama41/tfenv/blob/c859abc80bcab1cdb3b166df358e82ff7c1e1d36/README.md#usage
RUN apt-get update && apt-get install -yq curl unzip perl python3 git

RUN tfenv install 0.11.2 && \
    tfenv install 0.11.3 && \
    tfenv install 0.11.4

ENTRYPOINT ["/opt/mars/run.sh"]

ADD terraform.py /opt/mars/terraform.py
RUN chmod a+x /opt/mars/terraform.py
RUN ssh-keyscan -H bitbucket.org >> /etc/ssh/authorized_keys
ADD ssh_config /etc/ssh/ssh_config
ADD run.sh /opt/mars/run.sh
RUN chmod a+x /opt/mars/run.sh
