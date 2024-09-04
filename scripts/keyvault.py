import boto3
from azure.keyvault.secrets import SecretClient
from azure.identity import DefaultAzureCredential


def get_azure_secret(az_vault, az_vault_key):
    vault_uri = f"https://{az_vault}.vault.azure.net"
    credential = DefaultAzureCredential()
    client = SecretClient(vault_url=vault_uri, credential=credential)
    return client.get_secret(az_vault_key).value


def get_aws_secret(
    aws_secret_name,
    aws_region_name,
    role_arn=None,
    session_name="AssumeRoleVaultSession1",
):
    """
    Retrieve a secret from AWS Secrets Manager, optionally assuming a role before retrieval.

    :param aws_secret_name: The name of the secret to retrieve
    :param aws_region_name: The AWS region where the secret is stored
    :param role_arn: (Optional) The ARN of the role to assume
    :param session_name: (Optional) A name for the assumed session
    :return: The secret string from AWS Secrets Manager
    """
    if role_arn:
        # Assume the role
        sts_client = boto3.client("sts")
        assumed_role_object = sts_client.assume_role(
            RoleArn=role_arn, RoleSessionName=session_name
        )
        credentials = assumed_role_object["Credentials"]

        # Create a new session with the assumed role's credentials
        session = boto3.Session(
            aws_access_key_id=credentials["AccessKeyId"],
            aws_secret_access_key=credentials["SecretAccessKey"],
            aws_session_token=credentials["SessionToken"],
            region_name=aws_region_name,
        )
    else:
        # Use default session if no role assumption is needed
        session = boto3.session.Session()

    # Create the Secrets Manager client
    client = session.client(service_name="secretsmanager", region_name=aws_region_name)

    # Retrieve the secret
    response = client.get_secret_value(
        SecretId=aws_secret_name, VersionStage="AWSCURRENT"
    )
    return response["SecretString"]
