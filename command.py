#!/usr/bin/env python3

import os
import shlex
import subprocess
import sys

def shell_command(args):
    return ' '.join(shlex.quote(a) for a in args)

def script(*cmds, **kwargs):
    script = ' && '.join(map(shell_command, cmds))
    chdir = kwargs.get('chdir')
    cmd = '({})'.format(script)
    sys.stderr.write(cmd + '\n')
    if kwargs.get('dry_run'):
        return 0
    return subprocess.check_call(cmd, shell=True, cwd=chdir)

def run(*cmdargs, **kwargs):
    cmd = shell_command(cmdargs)
    sys.stderr.write(cmd + '\n')
    chdir = kwargs.get('chdir')
    return subprocess.check_call(cmd, shell=True, cwd=chdir)

def script_capture(*cmds, **kwargs):
    script = ' && '.join(map(shell_command, cmds))
    chdir = kwargs.get('chdir')
    cmd = '({})'.format(script)
    sys.stderr.write(cmd + '\n')
    return subprocess.check_output(['sh', '-c', script], shell=True, cwd=chdir)

def run_capture(*cmdargs, **kwargs):
    cmd = shell_command(cmdargs)
    chdir = kwargs.get('chdir')
    sys.stderr.write(cmd + '\n')
    return subprocess.check_output(cmd, shell=True, cwd=chdir)
