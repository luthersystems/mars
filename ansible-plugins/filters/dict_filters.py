class FilterModule(object):
    def filters(self):
        return {
            'dict_without_keys': self.dict_without_keys
        }

    def dict_without_keys(self, d, list_of_keys):
        '''
        Produces a copy of the input dict without any of the keys specified in
        list_of_keys.
        '''
        key_set = set(list_of_keys)
        return { k: v for (k, v) in d.items() if k not in key_set }
