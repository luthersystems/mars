#!/usr/bin/env python3

from glob import glob
import shlex
import os
import os.path
import re
import sys
import itertools
import textwrap
import tempfile
import yaml
import subprocess

# TODO: * add ENV var check


class Ansible(object):

    def __init__(self):
        self.env = 'develop'
        self.inventory_config = None
        self.inventory_defaults = {
            'ssh_user': 'ubuntu',
            'ssh_common_args': [
                '-oStrictHostKeyChecking=no',
                '-oUserKnownHostsFile=/dev/null',
            ],
            # "script" is an misnomer from when the inventory was a python
            # script.  The inventory source can be any file format/plugin
            # supported by ansible.
            'script': 'aws_ec2.yml',
        }
        self.vault_password_file = None
        self.playbook_default = 'site.yaml'

    def main(self):
        import argparse
        argparser = argparse.ArgumentParser()
        argparser.add_argument('env')
        subparsers = argparser.add_subparsers()

        execute_parser = subparsers.add_parser('ansible-execute')
        execute_parser.add_argument('host_pattern')
        execute_parser.add_argument('--module', '-m', default='ping')
        execute_parser.add_argument('--args', '-a')
        execute_parser.set_defaults(parser_func=self.execute)

        playbook_parser = subparsers.add_parser('ansible-playbook')
        playbook_parser.add_argument('path')
        playbook_parser.add_argument('--debug', action='store_true')
        playbook_parser.add_argument('--verbose', '-v', action='count', default=0)
        playbook_parser.add_argument('--tags')
        playbook_parser.add_argument('--limit')
        playbook_parser.add_argument('--check', action="store_true")
        playbook_parser.add_argument('--start-at-task')
        playbook_parser.set_defaults(parser_func=self.playbook)

        vault_edit_parser = subparsers.add_parser('ansible-vault-edit')
        vault_edit_parser.add_argument('path')
        vault_edit_parser.set_defaults(parser_func=self.vault_edit)

        vault_encrypt_parser = subparsers.add_parser('ansible-vault-encrypt')
        vault_encrypt_parser.add_argument('--name', '-n',
                                          help='converted to --stdin-name if --string not given')
        vault_encrypt_parser.add_argument('--string')
        vault_encrypt_parser.add_argument('--encrypt-raw-stdin', '-r', action='store_true')
        vault_encrypt_parser.set_defaults(parser_func=self.vault_encrypt)

        vault_decrypt_parser = subparsers.add_parser('ansible-vault-decrypt')
        vault_decrypt_parser.add_argument('--path')
        vault_decrypt_parser.set_defaults(parser_func=self.vault_decrypt)

        vault_view_parser = subparsers.add_parser('ansible-vault-view')
        vault_view_parser.add_argument('path')
        vault_view_parser.set_defaults(parser_func=self.vault_view)

        args = argparser.parse_args()

        if 'parser_func' not in vars(args):
            # no environment given
            sys.stderr.write('no sub-command given\n\n')
            argparser.print_help()
            exit(1)

        self.env = args.env
        args_ignore = set(['parser_func', 'env'])
        kwargs = {k: v for k, v in vars(args).items() if k not in args_ignore}
        args.parser_func(**kwargs)

    def playbook(self, path=None, tags=None, limit=None, check=False, debug=False, verbose=0, start_at_task=None):
        if not path:
            path = self.playbook_default

        base_cmd = ['ansible-playbook']
        if debug:
            base_cmd.append('-vvvv')
        elif verbose > 0:
            base_cmd.append('-' + 'v'*verbose)
        base_cmd.append(path)
        vault_args = self._ansible_vault_credentials()
        inv_args = self._inventory_args()
        user_args = self._ssh_user_args()
        ssh_common_args = self._ssh_common_args()
        extra_args = []
        if check:
            extra_args.append('--check')
        if tags:
            extra_args.append('--tags')
            extra_args.append(tags)
        if limit:
            extra_args.append('--limit')
            extra_args.append(limit)
        if start_at_task:
            extra_args.append('--start-at-task')
            extra_args.append(start_at_task)
        cmd = itertools.chain(
            base_cmd,
            vault_args,
            inv_args,
            user_args,
            ssh_common_args,
            extra_args
        )
        rc = self._exec_cmd(*cmd)
        exit(rc)

    def execute(self, host_pattern=None, module=None, args=None):
        if host_pattern is None:
            raise Exception('host_pattern not provided')
        if module is None:
            raise Exception('module not provided')

        base_cmd = ['ansible']
        vault_args = self._ansible_vault_credentials()
        inv_args = self._inventory_args()
        user_args = self._ssh_user_args()
        ssh_common_args = self._ssh_common_args()
        host_args = [host_pattern]
        module_args = ['-m', module]
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
        rc = self._exec_cmd(*cmd)
        exit(rc)

    def vault_edit(self, path=None):
        if path is None:
            raise Exception('path not provided')
        vault_args = self._ansible_vault_credentials()

        vault_action = 'edit'
        if not os.path.exists(path):
            vault_action = 'create'

        cmd = ['ansible-vault',
               vault_action,
               *vault_args,
               path]
        rc = self._exec_cmd(*cmd)
        exit(rc)

    def vault_view(self, path=None):
        if path is None:
            raise Exception('path not provided')
        vault_args = self._ansible_vault_credentials()

        vault_action = 'view'
        cmd = ['ansible-vault',
               vault_action,
               *vault_args,
               path]
        rc = self._exec_cmd(*cmd)
        exit(rc)

    def vault_encrypt(self, name=None, string=None, encrypt_raw_stdin=False):
        # Only check encrypt_raw_stdin when no string is provided.
        if string is None and not encrypt_raw_stdin:
            # Remove indenting and strip any trailing newline from input text to make strings
            # encrypted using pipes more user friendly in general.
            with tempfile.NamedTemporaryFile(mode='w') as f:
                raw = sys.stdin.read()
                cleartext = textwrap.dedent(raw).rstrip('\n')
                f.write(cleartext)
                f.flush()
                self._vault_encrypt_path(f.name, name=name)
                return

        base_cmd = ['ansible-vault']
        sub_cmd = ['encrypt_string']
        if name is not None:
            flag = '--name'
            if string is None:
                flag = '--stdin-name'
            sub_cmd.extend([flag, name])
        if string is not None:
            sub_cmd.append(str(string))
        cmd = itertools.chain(
            base_cmd,
            sub_cmd,
            self._ansible_vault_credentials(),
        )
        rc = self._exec_cmd(*cmd)
        exit(rc)

    def _vault_encrypt_path(self, path, name=None):
        base_cmd = ['ansible-vault']
        sub_cmd = ['encrypt_string']
        if name is not None:
            sub_cmd.extend(['--stdin-name', name])

        cmd = itertools.chain(
            base_cmd,
            sub_cmd,
            self._ansible_vault_credentials(),
        )
        rc = self._exec_script(cmd, stdin_file=path)
        exit(rc)

    def vault_decrypt(self, path=None):
        if path is not None:
            self._vault_decrypt(path)
        else:
            # Indenting whitespace needs to be removed in order for ansible-vault to be decrypt
            # single variables that have been encrypted using vault_encrypt.
            with tempfile.NamedTemporaryFile(mode='w') as f:
                raw = sys.stdin.read()
                # remove leading line with YAML label and vault header if present
                raw = re.sub("[^\n]+!vault \|\n", "", raw)
                encrypted = textwrap.dedent(raw)
                f.write(encrypted)
                f.flush()
                self._vault_decrypt(f.name)

    def _vault_decrypt(self, path):
        base_cmd = ['ansible-vault']
        sub_cmd = ['decrypt', '--output=-', path]

        cmd = itertools.chain(
            base_cmd,
            sub_cmd,
            self._ansible_vault_credentials(),
        )
        rc = self._exec_cmd(*cmd)
        exit(rc)

    def _read_inventory_config(self):
        if self.inventory_config is not None:
            # don't read again
            return
        config_path = os.path.join('inventories', self.env, 'mars.yaml')
        config = {}
        config.update(self.inventory_defaults)
        if os.path.exists(config_path):
            with open(config_path) as f:
                config.update(yaml.load(f))
        self.inventory_config = config

    def _ansible_vault_credentials(self):
        if self.vault_password_file is None:
            self.vault_password_file = self._find_ansible_vault_password_file()
        return ['--vault-password-file', self.vault_password_file]

    def _find_ansible_vault_password_file(self):
        files = glob('*_vault_password.txt')
        default_file = 'vault_password.txt'
        if os.path.exists(default_file):
            files.append(default_file)
        if len(files) == 0:
            raise Exception('vault password file could not be located')
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


if __name__ == '__main__':
    ans = Ansible()
    ans.main()
