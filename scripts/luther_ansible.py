#!/usr/bin/env python3

from glob import glob
import argparse
import shlex
import os
import os.path
import re
import stat
import sys
import itertools
import textwrap
import tempfile
import yaml
import subprocess

from ansible.constants import DEFAULT_VAULT_ID_MATCH # type: ignore
from ansible.parsing.vault import VaultLib, VaultSecret

import keyvault

# TODO: * add ENV var check


class Ansible(object):

    def __init__(self):
        self.env = ''
        self.inventory_config = {}
        self.inventory_defaults = {
            'ssh_user': 'ubuntu',
            'ssh_common_args': [
                '-oStrictHostKeyChecking=no',
                '-oUserKnownHostsFile=/dev/null',
            ],
            # 'script' is an misnomer from when the inventory was a python
            # script.  The inventory source can be any file format/plugin
            # supported by ansible.
            'script': 'aws_ec2.yml',
        }
        self.vault_password_file = None

    def main(self):
        argparser = argparse.ArgumentParser()
        argparser.add_argument('env')
        subparsers = argparser.add_subparsers()

        # common vault arguments
        vault_parser = argparse.ArgumentParser(add_help=False)
        vault_parser.add_argument('--az-vault', default='')
        vault_parser.add_argument('--az-vault-key', default='')

        playbook_parser = subparsers.add_parser('ansible-playbook', parents=[vault_parser])
        playbook_parser.add_argument('path')
        playbook_parser.add_argument('--debug', action='store_true')
        playbook_parser.add_argument('--verbose', '-v', action='count', default=0)
        playbook_parser.add_argument('--tags', default='')
        playbook_parser.add_argument('--limit', default='')
        playbook_parser.add_argument('--check', action='store_true')
        playbook_parser.add_argument('--start-at-task', default='')
        playbook_parser.set_defaults(func=self.playbook)

        execute_parser = subparsers.add_parser('ansible-execute', parents=[vault_parser])
        execute_parser.add_argument('host_pattern')
        execute_parser.add_argument('--module', '-m', default='ping')
        execute_parser.add_argument('--args', '-a', default='')
        execute_parser.set_defaults(func=self.execute)

        vault_encrypt_parser = subparsers.add_parser('ansible-vault-encrypt', parents=[vault_parser])
        vault_encrypt_parser.add_argument('--path', default='')
        vault_encrypt_parser.set_defaults(func=self.vault_encrypt)

        vault_decrypt_parser = subparsers.add_parser('ansible-vault-decrypt', parents=[vault_parser])
        vault_decrypt_parser.add_argument('--path', default='')
        vault_decrypt_parser.set_defaults(func=self.vault_decrypt)

        vault_decrypt_key_parser = subparsers.add_parser('ansible-vault-decrypt-key', parents=[vault_parser])
        vault_decrypt_key_parser.add_argument('yaml_file')
        vault_decrypt_key_parser.add_argument('key')
        vault_decrypt_key_parser.set_defaults(func=self.vault_decrypt_key)

        args = argparser.parse_args()

        if 'func' not in vars(args):
            # no environment given
            sys.stderr.write('no sub-command given\n\n')
            argparser.print_help()
            exit(1)

        az_vault_args = [args.az_vault, args.az_vault_key]
        if any(az_vault_args) and not all(az_vault_args):
            raise Exception('--az-vault and --az-vault-key must be supplied together')

        self.env = args.env
        args.func(args)

    def playbook(self, args):
        base_cmd = ['ansible-playbook']
        if args.debug:
            base_cmd.append('-vvvv')
        elif args.verbose > 0:
            base_cmd.append('-' + 'v'*args.verbose)
        base_cmd.append(args.path)
        vault_args = self._ansible_vault_args(args)
        inv_args = self._inventory_args()
        user_args = self._ssh_user_args()
        ssh_common_args = self._ssh_common_args()
        extra_args = []
        if args.check:
            extra_args.append('--check')
        if args.tags:
            extra_args.append('--tags')
            extra_args.append(args.tags)
        if args.limit:
            extra_args.append('--limit')
            extra_args.append(args.limit)
        if args.start_at_task:
            extra_args.append('--start-at-task')
            extra_args.append(args.start_at_task)
        cmd = itertools.chain(
            base_cmd,
            vault_args,
            inv_args,
            user_args,
            ssh_common_args,
            extra_args
        )
        rc = self._exec_cmd(*cmd, env=os.environ | _add_env_vars(args))
        exit(rc)

    def execute(self, args):
        base_cmd = ['ansible']
        vault_args = self._ansible_vault_args(args)
        inv_args = self._inventory_args()
        user_args = self._ssh_user_args()
        ssh_common_args = self._ssh_common_args()
        host_args = [args.host_pattern]
        module_args = ['-m', args.module]
        args_args = []
        if args:
            args_args = args
        cmd = itertools.chain(
            base_cmd,
            vault_args,
            inv_args,
            user_args,
            ssh_common_args,
            host_args,
            module_args,
            args_args
        )
        rc = self._exec_cmd(*cmd, env=os.environ | _add_env_vars(args))
        exit(rc)

    def vault_encrypt(self, args):
        encryption_key = self._get_encryption_key(args)
        if args.path:
            with open(args.path) as f:
                secret = f.read()
        else:
            if not stdin_pipe():
                raise Exception('expected to read a secret from stdin')
            secret = sys.stdin.read().rstrip('\n')
        self._vault_encrypt(secret, encryption_key)

    def _vault_encrypt(self, secret, encryption_key):
        vault = vault_client(encryption_key)
        encrypted = vault.encrypt(bytes(secret, 'utf-8'))
        print(encrypted.decode('utf-8'))

    def _get_encryption_key(self, args):
        if args.az_vault:
            return keyvault.get_secret(args.az_vault, args.az_vault_key)
        # fall back to password file
        return self._ansible_vault_encryption_key()

    def vault_decrypt_key(self, args):
        encryption_key = self._get_encryption_key(args)
        with open(args.yaml_file) as f:
            encrypted_keys = yaml.load(f, Loader=VaultLoader)
        encrypted = encrypted_keys[args.key]
        self._vault_decrypt(encrypted, encryption_key)

    def vault_decrypt(self, args):
        encryption_key = self._get_encryption_key(args)
        if args.path:
            with open(args.path) as f:
                encrypted = f.read()
            self._vault_decrypt(encrypted, encryption_key, filename=args.path)
        else:
            if not stdin_pipe():
                raise Exception('expected to read an encrypted secret from stdin')
            encrypted = sys.stdin.read().rstrip('\n')
            self._vault_decrypt(encrypted, encryption_key)

    def _vault_decrypt(self, encrypted, encryption_key, filename=None):
        vault = vault_client(encryption_key)
        secret = vault.decrypt(bytes(encrypted, 'utf-8'), filename)
        print(secret.decode('utf-8'))

    def _read_inventory_config(self):
        if self.inventory_config:
            # don't read again
            return
        config_path = os.path.join('inventories', self.env, 'mars.yaml')
        config = {}
        config.update(self.inventory_defaults)
        if os.path.exists(config_path):
            with open(config_path) as f:
                config.update(yaml.load(f, Loader=yaml.FullLoader))
        self.inventory_config = config

    def _ansible_vault_encryption_key(self):
        if self.vault_password_file is None:
            self.vault_password_file = self._find_ansible_vault_password_file()
        with open(self.vault_password_file) as f:
            return f.read()

    def _ansible_vault_args(self, args):
        if args.az_vault:
            return ['--vault-id', '/opt/mars/vault-az-keyvault.py']
        if self.vault_password_file is None:
            self.vault_password_file = self._find_ansible_vault_password_file()
        return ['--vault-password-file', self.vault_password_file]

    def _find_ansible_vault_password_file(self):
        files = glob('*_vault_password.txt')
        default_file = 'vault_password.txt'
        if os.path.exists(default_file):
            files.append(default_file)
        if len(files) == 0:
            raise Exception('no remote vault key supplied and vault password file could not be located')
        if len(files) > 1:
            raise Exception('unable to determine vault password file from ambiguous entries {}'.format(files))
        return files[0]

    def _inventory_args(self):
        self._read_inventory_config()
        script = self.inventory_config['script']
        if script is None:
            raise Exception('missing inventory script for env')
        full_path = os.path.join('inventories', self.env, script)
        return ['-i', full_path]

    def _ssh_user_args(self):
        self._read_inventory_config()
        user = self.inventory_config['ssh_user']
        if user is None:
            return []
        return ['-u', user]

    def _ssh_common_args(self):
        self._read_inventory_config()
        args = self.inventory_config['ssh_common_args']
        if args is None:
            return []
        quoted = (shlex.quote(a) for a in args)
        # TODO:  Figure out why --ssh-extra-args is used instead of
        # --ssh-common-args and fix naming.
        return ['--ssh-extra-args', ' '.join(args)]

    def _exec_script(self, *cmds, **kwargs):
        script = self._shell_script(*cmds)
        if kwargs.get('stdin_file') is not None:
            script += ' < {}'.format(shlex.quote(kwargs['stdin_file']))
        sys.stderr.write(script + '\n')
        return os.system(script)

    def _exec_cmd(self, *cmdargs, **kwargs):
        sys.stderr.write(self._shell_command(*cmdargs) + '\n')
        sys.stderr.flush()
        return subprocess.call(list(cmdargs), env=kwargs.get('env'))

    def _shell_script(self, *cmds):
        def shell_cmd(args):
            return self._shell_command(*args)
        cmds = map(shell_cmd, cmds)
        return '(' + ' && '.join(cmds) + ')'

    def _shell_command(self, *cmdargs):
        return ' '.join(shlex.quote(a) for a in cmdargs)

def stdin_pipe():
    return stat.S_ISFIFO(os.fstat(sys.stdin.fileno()).st_mode)

def vault_client(encryption_key):
    return VaultLib([
        (DEFAULT_VAULT_ID_MATCH,
         VaultSecret(bytes(encryption_key, 'utf-8')))
    ])

def _add_env_vars(args):
    if args.az_vault:
        return {
            "AZ_KEYVAULT_NAME": args.az_vault,
            "AZ_KEYVAULT_KEY": args.az_vault_key,
        }
    return {}

class VaultLoader(yaml.SafeLoader):
    pass

def _yaml_vault_constructor(loader, node):
    return loader.construct_scalar(node)

yaml.add_constructor('!vault', _yaml_vault_constructor, VaultLoader)

if __name__ == '__main__':
    ans = Ansible()
    ans.main()
