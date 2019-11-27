import os
import json

from ansible.errors import AnsibleError
from ansible.plugins.lookup import LookupBase


class LookupModule(LookupBase):

    def run(self, terms, variables, **kwargs):

        ret = []
        for path in terms:
            d = {}
            if not os.path.isdir(path):
                raise AnsibleError('path is not a directory: %s' % path)
            for name in os.listdir(path):
                fullpath = os.path.join(path, name)
                if not os.path.isfile(fullpath):
                    raise AnsibleError('directory entry is not a regular file: %s' % fullpath)
                try:
                    d[name] = json.load(open(fullpath, 'r'))
                except:
                    raise AnsibleError('failed to parse json file: %s' % fullpath)
            ret.append(d)
        return ret
