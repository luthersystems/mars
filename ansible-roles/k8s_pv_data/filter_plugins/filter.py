import builtins
from ansible.module_utils.common.collections import is_sequence

class FilterModule(object):
    def filters(self):
        filters = {
            'k8s_label_to_label_map': k8s_label_to_label_map,
        }

        return filters

def label_value(resource, label, type_='str'):
    convert = getattr(builtins, type_)
    return convert(resource["metadata"]["labels"][label])

# resources:
# - metadata:
#     labels:
#       label1: value1
#       label2: value2
def k8s_label_to_label_map(resources, key_label, key_type, value_label):
    '''takes a resource result (list of dicts) from k8s_info and processes the labels,
       returning a dict mapping from one label value to another'''

    if not is_sequence(resources):
        raise AnsibleFilterError("k8s_label_map requires a list, got %s instead." % type(resources))

    return {label_value(r, key_label, key_type):
            label_value(r, value_label)
            for r in resources}
