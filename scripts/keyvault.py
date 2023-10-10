from azure.keyvault.secrets import SecretClient
from azure.identity import AzureCliCredential

def get_secret(az_vault, az_vault_key):
    vault_uri = f'https://{az_vault}.vault.azure.net'
    credential = AzureCliCredential()
    client = SecretClient(vault_url=vault_uri, credential=credential)
    return client.get_secret(az_vault_key).value
