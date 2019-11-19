#!/usr/bin/env python3

import boto3
from dotenv import load_dotenv
import os
import sys

class ALBUtils(object):
    def __init__(self):
        self.alb_client = None
        self.verbose = False

    def _create_client(self, region=None):
        self.alb_client = boto3.client('elbv2', region_name=region)

    def dns_name(self, vars_file=None, region=None, project=None, component=None, org=None):
        if vars_file is not None:
            load_dotenv(vars_file)
        region = region or os.environ.get('AWS_REGION')
        project = project or os.environ.get('PROJECT_NAME')
        luther_env = self._luther_env()
        self._create_client(region=region)

        names = {}
        arns = []
        marker = None
        while True:
            args = {}
            if marker is not None:
                args['Marker'] = marker
            albs = self.alb_client.describe_load_balancers(**args)
            for alb in albs.get('LoadBalancers', []):
                arn = alb.get('LoadBalancerArn')
                arns.append(arn)
                names[arn] = alb.get('DNSName')
            marker = albs.get('Marker')
            if marker is None:
                break

        alb_tags = self.alb_client.describe_tags(ResourceArns=arns)
        for entry in alb_tags.get('TagDescriptions', []):
            tags = {}
            for tag_item in entry.get('Tags', []):
                tags[tag_item.get('Key')] = tag_item.get('Value')
            if self._matches_tags(tags, project=project, luther_env=luther_env, component=component, org=org):
                if self.verbose:
                    sys.stderr.write('{}\n'.format(tags))
                print(names[entry.get('ResourceArn')])

    def _matches_tags(self, tags, project=None, luther_env=None, component=None, org=None):
        if not self._matches_tag_value(tags, 'Project', project):
            return False
        if not self._matches_tag_value(tags, 'Environment', luther_env):
            return False
        if not self._matches_tag_value(tags, 'Component', component):
            return False
        if not self._matches_tag_value(tags, 'Organization', org):
            return False
        return True

    def _matches_tag_value(self, tags, tag_name, value):
        if value is None:
            return True
        return tags.get(tag_name) == value


    def _luther_env(self):
        if self.env == 'integration':
            return 'integ'
        return self.env


    def main(self):
        import argparse
        argparser = argparse.ArgumentParser()
        argparser.add_argument('env', help='The project environment to use')
        argparser.add_argument('--verbose', '-v', dest='verbosity', action='count', default=0)
        argparser.add_argument('--region', help="aws region containing albs")
        subparsers = argparser.add_subparsers()
        dns_parser = subparsers.add_parser('alb-dns')
        dns_parser.add_argument('--project', help='Only print albs with a matching project tag')
        dns_parser.add_argument('--component', help='Only print albs with a matching component tag')
        dns_parser.add_argument('--org', help='Only print albs with a matching organization tag')
        dns_parser.add_argument('--vars-file', '-f', help='File/Script with environment variable definitions')
        dns_parser.set_defaults(parser_func=self.dns_name)
        args = argparser.parse_args()

        self.env = args.env
        if args.verbosity > 0:
            self.verbose = True

        args_ignore = set(['parser_func', 'env', 'verbosity'])
        kwargs = {k: v for k, v in vars(args).items() if k not in args_ignore}

        args.parser_func(**kwargs)


if __name__ == '__main__':
    ALBUtils().main()
