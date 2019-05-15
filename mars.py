#!/usr/bin/env python3

import os
import sys
import terraform
import luther_ansible
import packer

class Mars(object):
    def main(self):
        args = sys.argv[1:]
        if len(args) == 0:
            sys.stderr.write('no arguments provided\n')
            os.exit(1)
        if len(args) < 2:
            sys.stderr.write('missing command\n')
            os.exit(1)
        command = args[1]
        # This is kind of a hack.  It might be best to use argparse here.
        if command.startswith('ansible-'):
            prog = luther_ansible.Ansible()
            prog.main()
        elif command.startswith('packer-'):
            prog = packer.Packer()
            prog.main()
        else:
            prog = terraform.Terraform()
            prog.main()

if __name__ == '__main__':
    Mars().main()
