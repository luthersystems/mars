#!/usr/bin/env python3

from glob import glob
import shlex
import tempfile
import os
import sys
import time
import itertools
import os.path


class Terraform(object):
    def __init__(self):
        self.var_dir = 'vars'
        self.shared_env = 'common'
        self._var_files = None
        self.verbosity = 0

    def main(self):
        import argparse
        argparser = argparse.ArgumentParser()
        argparser.add_argument('env')
        argparser.add_argument('--verbose', '-v', dest='verbosity', action='count', default=0)
        subparsers = argparser.add_subparsers()

        plan_parser = subparsers.add_parser('plan')
        plan_parser.add_argument('--destroy', action='store_true')
        plan_parser.add_argument('--out')
        plan_parser.add_argument('--apply', dest='apply_plan', action='store_true')
        plan_parser.set_defaults(parser_func=self.plan)

        apply_parser = subparsers.add_parser('apply')
        apply_parser.add_argument("--plan")
        apply_parser.set_defaults(parser_func=self.apply)

        destroy_parser = subparsers.add_parser('destroy')
        destroy_parser.set_defaults(parser_func=self.destroy)

        show_parser = subparsers.add_parser('show')
        show_parser.add_argument("--plan")
        show_parser.set_defaults(parser_func=self.show)

        graph_parser = subparsers.add_parser('graph')
        graph_parser.add_argument("--draw-cycles", action='store_true')
        graph_parser.set_defaults(parser_func=self.graph)

        init_parser = subparsers.add_parser('init')
        init_parser.add_argument("--backend-config",
                                 help='A backend config file or key=value assignment',
                                 nargs='+')
        init_parser.set_defaults(parser_func=self.init)

        taint_parser = subparsers.add_parser('taint')
        taint_parser.add_argument("name", help="A resource to taint", nargs='+')
        taint_parser.set_defaults(parser_func=self.taint)

        untaint_parser = subparsers.add_parser('untaint')
        untaint_parser.add_argument("name", help="A resource to untaint", nargs='+')
        untaint_parser.set_defaults(parser_func=self.untaint)

        args = argparser.parse_args()

        self.env = args.env
        self.verbosity = args.verbosity

        if self._verbosity_is(2):
            print("args: {}".format(args))

        if 'parser_func' not in vars(args):
            # no environment given
            sys.stderr.write('no sub-command given\n\n')
            argparser.print_help()
            exit(1)

        args_ignore = set(['parser_func', 'env', 'verbosity'])
        kwargs = {k: v for k, v in vars(args).items() if k not in args_ignore}
        args.parser_func(**kwargs)

    def init(self, backend_config=None):
        args = []
        if backend_config is not None:
            for config in backend_config:
                args.append('-backend-config')
                args.append(config)
        rc = self._script(
            # no switching workspaces for init -- not necessary
            ['terraform', 'init'] + list(args))
        exit(rc)

    def plan(self, destroy=False, out=None, apply_plan=False):
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

    def apply(self, plan=None):
        args = []
        if plan:
            args = [plan]
        else:
            args = self._var_file_args()
        rc = self._script(
            self._tf_workspace_select(),
            ['terraform', 'apply'] + list(args))
        exit(rc)

    def destroy(self):
        args = self._var_file_args()
        rc = self._script(
            self._tf_workspace_select(),
            ['terraform', 'destroy'] + list(args))
        exit(rc)

    def show(self, plan=None):
        args = []
        if plan:
            args = [plan]
        rc = self._script(
            self._tf_workspace_select(),
            ['terraform', 'show'] + list(args))
        exit(rc)

    def graph(self, draw_cycles=None):
        args = []
        if draw_cycles:
            args = ['-draw-cycles']
        rc = self._script(
            self._tf_workspace_select(),
            ['terraform', 'graph'] + list(args))
        exit(rc)

    def taint(self, name=None):
        names = name
        if isinstance(name, str):
            names = [name]
        for n in names:
            rc = self._script(
                self._tf_workspace_select(),
                ['terraform', 'taint', n])
            if rc != 0:
                exit(rc)

    def untaint(self, name=None):
        names = name
        if isinstance(name, str):
            names = [name]
        for n in names:
            rc = self._script(
                self._tf_workspace_select(),
                ['terraform', 'untaint', n])
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
        rc = os.system(cmd)
        return rc

    def _exec(self, *cmdargs, **kwargs):
        cmd = ' '.join(shlex.quote(a) for a in cmdargs)
        chdir = kwargs.get('chdir')
        if chdir:
            cmd = '(cd {} && {})'.format(chdir, cmd)
        sys.stderr.write(cmd + '\n')
        rc = os.system(cmd)
        return rc


if __name__ == '__main__':
    tf = Terraform()
    tf.main()
