class FilterModule(object):
    def filters(self):
        return {
            'luther_fabric_org_domain': self.luther_fabric_org_domain
        }

    def luther_fabric_org_domain(self, org, root_domain):
        '''
        Takes a root luthersystems domain and and org description (dict) to
        return the luther domain for the org.
        '''
        return "{}.{}".format(org['name'], root_domain)
