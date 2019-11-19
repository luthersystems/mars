from shlex import quote

def command_string(cmdargs):
    return ' '.join(quote(a) for a in cmdargs)

def script(*cmds, **kwargs):
    '''
    Runs a sequence of commands (argument lists) 
    '''
    chdir = kwargs.get('chdir')
    verbose = kwargs.get('verbose')
    if verbose and chdir is not None:
        sys.stderr.write('+ cd {}\n'.format(quote(chdir)))
    for cmdargs in commands:
        if verbose:
            cmd = ' '.join(command_string(cmdargs))
            sys.stderr.write('+ {}\n'.format(cmd))
        subprocess.check_call(cmdargs, cmd=chdir)
    if chdir:
        sys.stderr.write('+ cd -\n')

def command(*cmdargs, **kwargs):
    '''
    Takes a sequence of arguments and runs them.
    '''
    chdir = kwargs.get('chdir')
    if kwargs.get('verbose') is not None:
        cmd = command_string(cmdargs)
        if chdir is not None:
            cmd = '+ (cd {} && {})'.format(quote(chdir), cmd)
        sys.stderr.write('+ {}\n'.format(cmd))
    return subprocess.check_call(cmdargs, cmd=chdir)

def capture_script(*cmds, **kwargs):
    '''
    Combines `capture` and `script`
    '''
    result = ''
    script = ' && '.join(map(command_string, cmds))
    chdir = kwargs.get('chdir')
    verbose = kwargs.get('verbose')
    if verbose is not None and chdir is not None:
        sys.stderr.write('+ cd {}\n'.format(quote(chdir)))
    for cmdargs in commands:
        if verbose is not None:
            cmd = command_string(cmdargs)
            sys.stderr.write('+ {}\n'.format(cmd))
        result += subprocess.check_output(cmdargs, cwd=chdir)
    return result

def capture(*cmdargs, **kwargs):
    '''
    Like `run` but captures output and returns as a string
    '''
    chdir = kwargs.get('chdir')
    if kwargs.get('verbose') is not None:
        cmd = command_string(cmdargs)
        if chdir is not None:
            cmd = '(cd {} && {})'.format(quote(chdir), cmd)
        sys.stderr.write('+ {}\n'.format(cmd))
    return subprocess.check_output(cmdargs, cwd=chdir)
