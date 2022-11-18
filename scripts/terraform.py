#!/usr/bin/env python3

from glob import glob
import shlex
import tempfile
import os
import sys
import time
import itertools
import os.path
import subprocess


class Terraform(object):
    def __init__(self):
        self.var_dir = 'vars'
        self.shared_env = 'common'
        self._var_files = None
        self.verbosity = 0
        self.skip_prompt = False

    def main(self):
        import argparse
        argparser = argparse.ArgumentParser()
        argparser.add_argument('env')
        argparser.add_argument('--verbose', '-v', dest='verbosity', action='count', default=0)
        argparser.add_argument('--skip-prompt', action='store_true',
                               help='Skip workspace switch prompt')
        subparsers = argparser.add_subparsers()

        plan_parser = subparsers.add_parser('plan')
        plan_parser.add_argument('--destroy', action='store_true')
        plan_parser.add_argument('--out')
        plan_parser.add_argument('--target', action='append')
        plan_parser.add_argument('--apply', dest='apply_plan', action='store_true')
        plan_parser.add_argument('--refresh-only', action='store_true')
        plan_parser.set_defaults(parser_func=self.plan)

        apply_parser = subparsers.add_parser('apply')
        apply_parser.add_argument("--plan")
        apply_parser.add_argument("--target")
        apply_parser.add_argument("--approve", action='store_true')
        apply_parser.add_argument('--refresh-only', action='store_true')
        apply_parser.set_defaults(parser_func=self.apply)

        destroy_parser = subparsers.add_parser('destroy')
        destroy_parser.add_argument("--approve", action='store_true')
        destroy_parser.set_defaults(parser_func=self.destroy)

        show_parser = subparsers.add_parser('show')
        show_parser.add_argument("--plan")
        show_parser.set_defaults(parser_func=self.show)

        graph_parser = subparsers.add_parser('graph')
        graph_parser.add_argument("--draw-cycles", action='store_true')
        graph_parser.set_defaults(parser_func=self.graph)

        init_parser = subparsers.add_parser('init')
        init_parser.add_argument("--upgrade", action='store_true')
        init_parser.add_argument("--backend-config",
                                 help='A backend config file or key=value assignment',
                                 nargs='+')
        init_parser.set_defaults(parser_func=self.init)

        new_workspace_parser = subparsers.add_parser('new-workspace')
        new_workspace_parser.set_defaults(parser_func=self.new_workspace)

        taint_parser = subparsers.add_parser('taint')
        taint_parser.add_argument("--module", help='Module containing the resource to taint')
        taint_parser.add_argument("name", help="A resource to taint", nargs='+')
        taint_parser.set_defaults(parser_func=self.taint)

        untaint_parser = subparsers.add_parser('untaint')
        untaint_parser.add_argument("--module", help='Module containing the resource to untaint')
        untaint_parser.add_argument("name", help="A resource to untaint", nargs='+')
        untaint_parser.set_defaults(parser_func=self.untaint)

        import_parser = subparsers.add_parser('import')
        import_parser.add_argument("--allow-missing-config", action='store_true',
                                   help='Allow import when no resource configuration block exists.')
        import_parser.add_argument("addr", help="Address to import resource to")
        import_parser.add_argument("resource_id", help="Resource-specific ID")
        import_parser.set_defaults(parser_func=self.import_action)

        terraform_parser = subparsers.add_parser('terraform')
        terraform_parser.add_argument("args", help="A resource to untaint", nargs='+')
        terraform_parser.set_defaults(parser_func=self.terraform)

        migration_fromplan_parser = subparsers.add_parser('migration_fromplan')
        migration_fromplan_parser.add_argument("tfplan_file", help="input from terraform plan -out")
        migration_fromplan_parser.add_argument("migration_file", help="output HCL migration file")
        migration_fromplan_parser.set_defaults(parser_func=self.migration_fromplan)

        migrate_plan_parser = subparsers.add_parser('migrate_plan')
        migrate_plan_parser.add_argument("migration_file", help="HCL migration file")
        migrate_plan_parser.set_defaults(parser_func=self.migrate_plan)

        migrate_apply_parser = subparsers.add_parser('migrate_apply')
        migrate_apply_parser.add_argument("migration_file", help="HCL migration file")
        migrate_apply_parser.set_defaults(parser_func=self.migrate_apply)

        args = argparser.parse_args()

        self.env = args.env
        self.verbosity = args.verbosity
        self.skip_prompt = args.skip_prompt

        if self._verbosity_is(2):
            print("args: {}".format(args))

        if 'parser_func' not in vars(args):
            # no environment given
            sys.stderr.write('no sub-command given\n\n')
            argparser.print_help()
            exit(1)

        args_ignore = set(['parser_func', 'env', 'verbosity', 'skip_prompt'])
        kwargs = {k: v for k, v in vars(args).items() if k not in args_ignore}
        args.parser_func(**kwargs)

    def init(self, backend_config=None, upgrade=None):
        self._tfenv_init()
        args = []
        if backend_config is not None:
            for config in backend_config:
                args.append('-backend-config')
                args.append(config)
        if upgrade:
            args.append("--upgrade")
        rc = self._script(
            # no switching workspaces for init -- not necessary
            ['terraform', 'init'] + list(args))
        exit(rc)

    def new_workspace(self):
        self._tfenv_init()
        rc = self._script(['terraform', 'workspace', 'new', self.env])
        exit(rc)

    def terraform(self, args):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        cmd = ['terraform']
        cmd.extend(args)
        rc = self._script(
            self._tf_workspace_select(),
            cmd)
        if rc != 0:
            exit(rc)

    def plan(self, destroy=False, out=None, apply_plan=False, target=None, refresh_only=None):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        plan_path = out
        if apply_plan and plan_path is None:
            plan_dir = ".tf-plans"
            os.makedirs(plan_dir, exist_ok=True)
            plan_name_prefix = "tf-plan-{}-{}-".format(self.env, int(time.time()))
            plan_file, plan_path = tempfile.mkstemp(
                prefix=plan_name_prefix,
                suffix=".out",
                dir=plan_dir)
            os.close(plan_file)

        base_args = ['terraform', 'plan']
        var_file_args = self._var_file_args()
        extra_args = []
        if destroy:
            extra_args.append('-destroy')
        if plan_path is not None:
            arg = '-out={}'.format(plan_path)
            extra_args.append(arg)
        if target is not None:
            for t in target:
                extra_args.extend(['-target', t])
        if refresh_only:
            extra_args.append('-refresh-only')
        args = itertools.chain(base_args, var_file_args, extra_args)
        rc = self._script(
            self._tf_workspace_select(),
            args)
        if apply_plan:
            if rc != 0:
                print("Aborted -- planning failed")
                exit(rc)
            print()
            print("Would you like to apply this plan?")
            cont = ''
            while cont != 'yes':
                print("You must answer 'yes' to continue")
                try:
                    cont = input('> ')
                except EOFError:
                    print()
                    cont = 'no'
                if cont == 'no':
                    print("Aborted -- plan will not be applied")
                    print("path: {}".format(plan_path))
                    exit(1)
            print()
            self.apply(plan=plan_path)
        exit(rc)

    def apply(self, plan=None, target=None, refresh_only=None, approve=None):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        args = []
        if plan:
            args = [plan]
        else:
            args = list(self._var_file_args())
        if target is not None:
            args.extend(['-target', target])
        if refresh_only:
            args.append('-refresh-only')
        if approve:
            args.append('-auto-approve')
        rc = self._script(
            self._tf_workspace_select(),
            ['terraform', 'apply'] + list(args))
        exit(rc)

    def destroy(self, approve=None):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        args = list(self._var_file_args())
        if approve:
            args.append('-auto-approve')
        rc = self._script(
            self._tf_workspace_select(),
            ['terraform', 'destroy'] + list(args))
        exit(rc)

    def show(self, plan=None):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        args = []
        if plan:
            args = [plan]
        rc = self._script(
            self._tf_workspace_select(),
            ['terraform', 'show'] + list(args))
        exit(rc)

    def graph(self, draw_cycles=None):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        args = []
        if draw_cycles:
            args = ['-draw-cycles']
        rc = self._script(
            self._tf_workspace_select(),
            ['terraform', 'graph'] + list(args))
        exit(rc)

    def import_action(self, allow_missing_config=None, addr=None, resource_id=None):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        base_args = ['terraform', 'import']
        var_file_args = self._var_file_args()
        extra_args = []
        if allow_missing_config:
            extra_args.extend(['-allow-missing-config'])
        args = itertools.chain(base_args, var_file_args, extra_args, [addr, resource_id])
        rc = self._script(
            self._tf_workspace_select(),
            args)
        exit(rc)

    def taint(self, name=None, module=None):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        names = name
        if isinstance(name, str):
            names = [name]
        cmd_base = ['terraform', 'taint']
        if module is not None:
            cmd_base.extend(['-module', module])
        for n in names:
            cmd = cmd_base + [n]
            rc = self._script(
                self._tf_workspace_select(),
                cmd)
            if rc != 0:
                exit(rc)

    def untaint(self, name=None, module=None):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        names = name
        if isinstance(name, str):
            names = [name]
        cmd_base = ['terraform', 'untaint']
        if module is not None:
            cmd_base.extend(['-module', module])
        for n in names:
            cmd = cmd_base + [n]
            rc = self._script(
                self._tf_workspace_select(),
                cmd)
            if rc != 0:
                exit(rc)

    def migration_fromplan(self, tfplan_file=None, migration_file=None):
        self._tfenv_init()
        show_cmd = ("terraform", "show", "-json", tfplan_file)
        tfedit_cmd = ("tfedit", "migration", "fromplan", "-o="+migration_file)
        tfshow = subprocess.Popen(show_cmd, stdout=subprocess.PIPE)
        subprocess.check_call(tfedit_cmd, stdin=tfshow.stdout)
        tfshow.wait()

    def migrate_plan(self, migration_file=None):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        var_file_args = " ".join(self._var_file_args())
        cmd = ("tfmigrate", "plan", migration_file)
        environ = os.environ.copy()
        environ["TFMIGRATE_LOG"] = "DEBUG"
        environ["TF_CLI_ARGS_plan"] = var_file_args
        environ["TF_CLI_ARGS_import"] = var_file_args
        subprocess.check_call(self._tf_workspace_select())
        subprocess.check_call(cmd, env=environ)

    def migrate_apply(self, migration_file=None):
        self._tfenv_init()
        self._check_env()
        self._prompt_env_switch()
        var_file_args = " ".join(self._var_file_args())
        cmd = ("tfmigrate", "apply", migration_file)
        environ = os.environ.copy()
        environ["TFMIGRATE_LOG"] = "DEBUG"
        environ["TF_CLI_ARGS_plan"] = var_file_args
        environ["TF_CLI_ARGS_apply"] = var_file_args
        environ["TF_CLI_ARGS_import"] = var_file_args
        subprocess.check_call(self._tf_workspace_select())
        subprocess.check_call(cmd, env=environ)

    def _check_env(self):
        '''
        We want to fail most commands if the environment doesn't exist.  In the past this was
        harmless but under new deployment methods you can end up in the wrong workspace for the
        subproject you are in.
        '''
        env_dir = os.path.join(self.var_dir, self.env)
        if not os.path.exists(env_dir):
            sys.stderr.write('\nNo environment in this project: {}\n\n.'.format(self.env))
            exit(1)

    def _prompt_env_switch(self):
        # FIXME:  Get the real current environment
        curr_env = self._exec_capture('terraform', 'workspace', 'show')
        curr_env = curr_env.decode('utf-8').strip()
        if curr_env == self.env:
            return
        sys.stderr.write(
                '\nswitching environment \x1b[31m{}\x1b[0m ~> \x1b[32m{}\x1b[0m\n\n'.format(curr_env, self.env))
        if self.skip_prompt:
            return
        while 1:
            sys.stderr.write("switch to {}? [y/N] ".format(self.env))
            resp = input()
            if resp:
                resp = resp.lower()
            else:
                resp = 'n'
            if resp == 'n' or resp == 'no':
                exit(1)
            if resp == 'y' or resp == 'yes':
                break
            sys.stderr.write('what?\n')

    def _tfenv_init(self):
        if not os.path.exists('.terraform-version'):
            # There is no specified version so we will use the default installed in the container.
            warn = 'WARNING: .terraform-version not found -- using the default installed terraform'
            sys.stderr.write('\n' + warn + '\n\n')
            return
        rc = self._script(["tfenv", "install"])
        if rc != 0:
            exit(rc)

    def _locate_var_files(self):
        if self._var_files is None:
            common_files = self._all_var_files(os.path.join(self.var_dir, self.shared_env))
            common_files = [os.path.join(self.shared_env, path) for path in common_files]
            env_files = self._all_var_files(os.path.join(self.var_dir, self.env))
            env_files = [os.path.join(self.env, path) for path in env_files]
            self._var_files = common_files + env_files
            if self._verbosity_is(1):
                self._log_var_files()
        return (os.path.join(self.var_dir, rel_path) for rel_path in self._var_files)

    def _all_var_files(self, d):
            pattern = os.path.join(d, '*.tfvars')
            return [os.path.basename(path) for path in glob(pattern)]

    def _log_var_files(self):
        for name in self._var_files:
            print("Variable file: {}".format(name))

    def _var_file_args(self):
        paths = list(self._locate_var_files())
        return ("-var-file={}".format(path) for path in paths)

    def _verbosity_is(self, level):
        return self.verbosity >= level

    def _tf_workspace_select(self, name=None):
        if name is None:
            name = self.env
        return ['terraform', 'workspace', 'select', name]

    def _script(self, *cmds, **kwargs):
        def mkcmd(args):
            return ' '.join(shlex.quote(a) for a in args)
        script = ' && '.join(map(mkcmd, cmds))
        chdir = kwargs.get('chdir')
        if chdir:
            cd = 'cd {}'.format(shlex.quote(chdir))
            script = '{} && {}'.format(cd, script)
        cmd = '({})'.format(script)
        sys.stderr.write(cmd + '\n')
        if kwargs.get('dry_run'):
            return 0
        return self._sh(cmd)

    def _exec(self, *cmdargs, **kwargs):
        cmd = ' '.join(shlex.quote(a) for a in cmdargs)
        chdir = kwargs.get('chdir')
        if chdir:
            cmd = '(cd {} && {})'.format(chdir, cmd)
        sys.stderr.write(cmd + '\n')
        return self._sh(cmd)

    def _script_capture(self, *cmds, **kwargs):
        def mkcmd(args):
            return ' '.join(shlex.quote(a) for a in args)
        script = ' && '.join(map(mkcmd, cmds))
        chdir = kwargs.get('chdir')
        cmd = '({})'.format(script)
        sys.stderr.write(cmd + '\n')
        return subprocess.check_output(['sh', '-c', script], shell=True, cwd=chdir)

    def _exec_capture(self, *cmdargs, **kwargs):
        cmd = ' '.join(shlex.quote(a) for a in cmdargs)
        chdir = kwargs.get('chdir')
        sys.stderr.write(cmd + '\n')
        return subprocess.check_output(cmd, shell=True, cwd=chdir)

    def _sh(self, cmd):
        process = subprocess.Popen(['/bin/bash', '-c', cmd], stdin=sys.stdin, stderr=sys.stderr)
        process.communicate()
        rc = process.wait()
        return rc

if __name__ == '__main__':
    tf = Terraform()
    tf.main()
