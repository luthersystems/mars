#!/usr/bin/env python3

import command

class Packer(object):
    def __init__(self):
        pass

    def validate(self, image=None):
        command.run('packer', 'validate', 'packer.json', chdir=image)

    def build(self, image=None, debug=False):
        cmd = ['packer', 'build']
        if debug:
            cmd.append('-debug')
        cmd.append('packer.json')
        command.run(*cmd, chdir=image)

    def main(self):
        import argparse
        argparser = argparse.ArgumentParser()
        argparser.add_argument('image')
        subparsers = argparser.add_subparsers()
        validate_parser = subparsers.add_parser('packer-validate')
        validate_parser.set_defaults(parser_func=self.validate)
        build_parser = subparsers.add_parser('packer-build')
        build_parser.add_argument('--debug', action='store_true')
        build_parser.set_defaults(parser_func=self.build)

        args = argparser.parse_args()

        if 'parser_func' not in vars(args):
            # no environment given
            sys.stderr.write('no sub-command given\n\n')
            argparser.print_help()
            exit(1)

        args_ignore = set(['parser_func'])
        kwargs = {k: v for k, v in vars(args).items() if k not in args_ignore}
        args.parser_func(**kwargs)



if __name__ == '__main__':
    Packer().main()
