#!/usr/bin/env python3
"""Refuse a GHCR version collision; authorization or network uncertainty fails closed."""
import argparse
import base64
import json
import os
from pathlib import Path
import re
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError('Unexpected registry redirect; no credentials were forwarded')


def assert_absent(repository, tag):
    if repository not in ('ziad-hsn/cpra', 'ziad-hsn/charts/cpra') or not re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}', tag):
        raise ValueError('Invalid canonical registry repository or tag')
    if not os.environ.get('GH_TOKEN') or not os.environ.get('GITHUB_ACTOR'):
        raise ValueError('Authenticated registry inspection is required before version publication')
    opener = urllib.request.build_opener(NoRedirect())
    credentials = base64.b64encode((os.environ['GITHUB_ACTOR']+':'+os.environ['GH_TOKEN']).encode()).decode()
    query = urllib.parse.urlencode({'service': 'ghcr.io', 'scope': 'repository:'+repository+':pull'})
    request = urllib.request.Request('https://ghcr.io/token?'+query, headers={'Authorization': 'Basic '+credentials})
    with opener.open(request, timeout=20) as response:
        token = json.load(response)['token']
    request = urllib.request.Request('https://ghcr.io/v2/'+repository+'/manifests/'+tag,
        headers={'Authorization': 'Bearer '+token, 'Accept': 'application/vnd.oci.image.manifest.v1+json,application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.v2+json,application/vnd.docker.distribution.manifest.list.v2+json'})
    try:
        with opener.open(request, timeout=20):
            raise ValueError('Registry version already exists and must never be overwritten: '+repository+':'+tag)
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise ValueError('Registry absence was not established (HTTP '+str(error.code)+')') from None
        try:
            errors = json.loads(error.read(1024*1024))['errors']
        except (ValueError, KeyError):
            raise ValueError('Registry returned an unrecognized missing-manifest response') from None
        if not errors or any(item.get('code') not in ('MANIFEST_UNKNOWN', 'NAME_UNKNOWN') for item in errors):
            raise ValueError('Registry missing-manifest response does not prove version absence')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest', type=Path, default=Path('dist/release/RELEASE.json'))
    args = parser.parse_args(); manifest = json.loads(args.manifest.read_text())
    assert_absent('ziad-hsn/cpra', manifest['version'])
    assert_absent('ziad-hsn/charts/cpra', manifest['chart_version'])


if __name__ == '__main__':
    main()
