#!/usr/bin/env python3

import os

import keyvault

# load AWS Secrets Manager name and region from environment variables

secret_id = os.environ["AWS_SM_SECRET_ID"]
aws_region = os.environ.get("AWS_REGION", "us-west-2")
role_arn = os.environ.get("AWS_ROLE_ARN")

secret = keyvault.get_aws_secret(secret_id, aws_region, role_arn=role_arn)
print(secret, end="")
