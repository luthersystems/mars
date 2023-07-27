#!/usr/bin/env python3

import os

import keyvault

# load Azure Key Vault name and key from environment variables
vault = os.environ["AZ_KEYVAULT_NAME"]
key = os.environ["AZ_KEYVAULT_KEY"]

secret = keyvault.get_secret(vault, key)
print(secret, end="")