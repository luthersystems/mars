def org_peer_pairs(orgs):
    return [(org['name'], i)
            for org in orgs
            for i in range(0, org['peers'])]

class FilterModule(object):
    def filters(self):
      return {'org_peer_pairs': org_peer_pairs}
