FROM ubuntu:16.04

ADD terraform.py /opt/mars/terraform.py
RUN chmod a+x /opt/mars/terraform.py

ADD build/tfenv /opt/tfenv
ENV PATH="/opt/tfenv/bin:${PATH}"

RUN mkdir -p /terraform /opt/home
ENV HOME="/opt/home"

WORKDIR /terraform
ENTRYPOINT ["/opt/mars/terraform.py"]

# Update apt cache and install prerequisites before running tfenv for the first
# time.
#   https://github.com/kamatama41/tfenv/blob/c859abc80bcab1cdb3b166df358e82ff7c1e1d36/README.md#usage
RUN apt-get update && apt-get install -yq curl unzip perl python3

RUN tfenv install 0.11.2
