#!/usr/bin/env python3
"""Prepare, but do not deploy, a CP static-dashboard CloudFormation update.
Keep the fetched template private: it can contain existing secret references.
Adopt live distribution aliases/TLS/VPC origins rather than reverting manual drift.
The existing stack must be deployed using previous values for existing parameters.
"""
import argparse
import copy
import json
from pathlib import Path
import re

PATHS = ('/ui/', '/ui/index.html', '/ui/app.js', '/ui/style.css')
CACHE_DISABLED = '4135ea2d-6df8-44a3-9df3-4b5a84be39ad'


def load_template(path):
    value = json.loads(Path(path).read_text())
    if 'TemplateBody' in value:
        value = value['TemplateBody']
    if isinstance(value, str):
        try:
            value = json.loads(value)
        except json.JSONDecodeError:
            # Authoring dependency only; never execute YAML tags. Preserve
            # scalar GetAtt syntax: normalizing it can replace ECS TaskDefs.
            import yaml
            class Loader(yaml.SafeLoader):
                pass
            def tagged(loader, tag, node):
                body = loader.construct_scalar(node) if isinstance(node, yaml.ScalarNode) else loader.construct_sequence(node) if isinstance(node, yaml.SequenceNode) else loader.construct_mapping(node)
                return {('Ref' if tag == 'Ref' else 'Fn::' + tag): body}
            Loader.add_multi_constructor('!', tagged)
            value = yaml.load(value, Loader=Loader)
    if not isinstance(value, dict) or 'Resources' not in value:
        raise ValueError('Expected an original CloudFormation template or get-template response')
    return value


def prepare(template, live, outputs, release):
    if not re.fullmatch(r'releases/[0-9a-f]{16,64}', release):
        raise ValueError('Use a content-hashed releases/<hex> prefix')
    before = copy.deepcopy(template)
    cfg = live['DistributionConfig'] if 'DistributionConfig' in live else live
    if cfg.get('OriginGroups', {}).get('Quantity', 0) or cfg.get('ContinuousDeploymentPolicyId') or cfg.get('Staging'):
        raise ValueError('Origin groups or staging/continuous deployment require a separate review')
    default = cfg['DefaultCacheBehavior']
    if default.get('FunctionAssociations', {}).get('Quantity', 0) or default.get('LambdaFunctionAssociations', {}).get('Quantity', 0):
        raise ValueError('Do not overwrite an existing edge function')
    result = copy.deepcopy(template)
    dist = result['Resources']['Distribution']['Properties']['DistributionConfig']
    # Preserve the live VPC-origin type and settings. Never regress to the older
    # public CustomOriginConfig stored in this stack's historical template.
    old_origins = {o['Id']: o for o in dist['Origins']}
    if set(old_origins) != {o['Id'] for o in cfg['Origins']['Items']}:
        raise ValueError('Live origin IDs differ; reconcile the stack before attaching assets')
    origins = []
    for origin in cfg['Origins']['Items']:
        o = {k: copy.deepcopy(origin[k]) for k in ('Id', 'DomainName', 'OriginPath', 'ConnectionAttempts', 'ConnectionTimeout') if k in origin}
        o['DomainName'] = copy.deepcopy(old_origins[origin['Id']]['DomainName'])
        if origin.get('CustomHeaders', {}).get('Quantity', 0):
            o['OriginCustomHeaders'] = copy.deepcopy(origin['CustomHeaders']['Items'])
        if origin.get('OriginAccessControlId'):
            o['OriginAccessControlId'] = origin['OriginAccessControlId']
        if origin.get('OriginShield', {}).get('Enabled'):
            o['OriginShield'] = copy.deepcopy(origin['OriginShield'])
        if 'VpcOriginConfig' in origin:
            o['VpcOriginConfig'] = copy.deepcopy(origin['VpcOriginConfig'])
        elif 'CustomOriginConfig' in origin:
            c = copy.deepcopy(origin['CustomOriginConfig'])
            if 'OriginSslProtocols' in c:
                c['OriginSSLProtocols'] = c.pop('OriginSslProtocols').get('Items', [])
            o['CustomOriginConfig'] = c
        elif 'S3OriginConfig' in origin:
            o['S3OriginConfig'] = copy.deepcopy(origin['S3OriginConfig'])
        else:
            raise ValueError('Unknown live origin type')
        origins.append(o)
    for key in ('Enabled', 'Comment', 'DefaultRootObject', 'HttpVersion', 'PriceClass', 'WebACLId'):
        if key in cfg:
            dist[key] = copy.deepcopy(cfg[key])
    dist['IPV6Enabled'] = cfg.get('IsIPV6Enabled', False)
    dist['Aliases'] = copy.deepcopy(cfg.get('Aliases', {}).get('Items', []))
    cert = cfg['ViewerCertificate']
    dist['ViewerCertificate'] = {cf: cert[api] for api, cf in (
        ('CloudFrontDefaultCertificate', 'CloudFrontDefaultCertificate'),
        ('ACMCertificateArn', 'AcmCertificateArn'), ('IAMCertificateId', 'IamCertificateId'),
        ('SSLSupportMethod', 'SslSupportMethod'), ('MinimumProtocolVersion', 'MinimumProtocolVersion'),
    ) if api in cert}
    if cert.get('ACMCertificateArn') or cert.get('IAMCertificateId'):
        # The API emits false alongside ACM/IAM, but CloudFormation treats
        # the presence of this field (even false) as a conflicting selector.
        dist['ViewerCertificate'].pop('CloudFrontDefaultCertificate', None)
    if cfg.get('Logging', {}).get('Enabled'):
        dist['Logging'] = {k: cfg['Logging'][k] for k in ('Bucket', 'IncludeCookies', 'Prefix')}
    if cfg.get('CustomErrorResponses', {}).get('Quantity', 0):
        dist['CustomErrorResponses'] = copy.deepcopy(cfg['CustomErrorResponses']['Items'])
    geo = cfg.get('Restrictions', {}).get('GeoRestriction', {})
    dist['Restrictions'] = {'GeoRestriction': {'RestrictionType': geo.get('RestrictionType', 'none')}}
    if geo.get('Quantity', 0):
        dist['Restrictions']['GeoRestriction']['Locations'] = copy.deepcopy(geo['Items'])
    # Auth/API behaviors and cache policy stay precisely as declared. Default
    # receives only the root redirect function; this function returns every
    # other request unchanged and never examines credentials or query values.
    existing_paths = {b['PathPattern'] for b in dist.get('CacheBehaviors', [])}
    if existing_paths.intersection(PATHS) or any(o['Id'] == 'dashboard-assets' for o in origins):
        raise ValueError('Assets already attached; use an explicitly reviewed release update')
    values = {
        'DashboardAssetsDomain': outputs['AssetsDomain'],
        'DashboardAccessControlId': outputs['AccessControlId'],
        'DashboardSecurityHeadersId': outputs['SecurityHeadersId'],
        'DashboardEntryFunctionArn': outputs['EntryFunctionArn'],
        'DashboardReleasePrefix': release,
    }
    patterns = {
        'DashboardAssetsDomain': r'[A-Za-z0-9.-]+\.amazonaws\.com',
        'DashboardAccessControlId': r'[A-Z0-9]+',
        'DashboardSecurityHeadersId': r'[0-9a-f-]+',
        'DashboardEntryFunctionArn': r'arn:[a-z-]+:cloudfront::[0-9]{12}:function/[A-Za-z0-9_-]+',
        'DashboardReleasePrefix': r'releases/[0-9a-f]{16,64}',
    }
    for key, value in values.items():
        if not re.fullmatch(patterns[key], value):
            raise ValueError('Invalid ' + key)
        result.setdefault('Parameters', {})[key] = {'Type': 'String', 'AllowedPattern': patterns[key]}
    origins.append({'Id': 'dashboard-assets', 'DomainName': {'Ref': 'DashboardAssetsDomain'},
                    'OriginPath': {'Fn::Sub': '/${DashboardReleasePrefix}'},
                    'OriginAccessControlId': {'Ref': 'DashboardAccessControlId'},
                    'S3OriginConfig': {'OriginAccessIdentity': ''}})
    dist['Origins'] = origins
    static = [{'PathPattern': path, 'TargetOriginId': 'dashboard-assets', 'ViewerProtocolPolicy': 'redirect-to-https',
               'AllowedMethods': ['GET', 'HEAD'], 'CachedMethods': ['GET', 'HEAD'], 'Compress': True,
               'CachePolicyId': CACHE_DISABLED, 'ResponseHeadersPolicyId': {'Ref': 'DashboardSecurityHeadersId'}} for path in PATHS]
    dist['CacheBehaviors'] = static + dist.get('CacheBehaviors', [])
    dist['DefaultCacheBehavior']['FunctionAssociations'] = [
        {'EventType': 'viewer-request', 'FunctionARN': {'Ref': 'DashboardEntryFunctionArn'}}]
    # No change to CP/mayu services, role permissions, identity config, policy
    # store, durable liabilities, token forwarding, or network infrastructure.
    for key, resource in before['Resources'].items():
        if key != 'Distribution' and result['Resources'][key] != resource:
            raise ValueError('Unexpected change to ' + key)
    return result, values


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('template', 'distribution-config', 'support-outputs', 'release-prefix', 'output'):
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    outputs = json.loads(Path(args.support_outputs).read_text())
    if isinstance(outputs, list):
        outputs = {v['OutputKey']: v['OutputValue'] for v in outputs}
    result, values = prepare(load_template(args.template),
                             json.loads(Path(args.distribution_config).read_text()), outputs, args.release_prefix)
    path = Path(args.output)
    path.write_text(json.dumps(result, indent=2) + '\n')
    path.chmod(0o600)
    params = [{'ParameterKey': k, 'ParameterValue': v} if k in values else {'ParameterKey': k, 'UsePreviousValue': True}
              for k, v in values.items()]
    params = [{'ParameterKey': k, 'UsePreviousValue': True} for k in result.get('Parameters', {}) if k not in values] + params
    param_path = path.with_suffix('.parameters.json')
    param_path.write_text(json.dumps(params, indent=2) + '\n')
    param_path.chmod(0o600)
    # Roll back to the adopted LIVE baseline, never to the historical template
    # that would remove the domain/certificate or undo private VPC origins.
    rollback = copy.deepcopy(result)
    dist = rollback['Resources']['Distribution']['Properties']['DistributionConfig']
    dist['Origins'] = [o for o in dist['Origins'] if o['Id'] != 'dashboard-assets']
    dist['CacheBehaviors'] = [b for b in dist['CacheBehaviors'] if b['PathPattern'] not in PATHS]
    dist['DefaultCacheBehavior'].pop('FunctionAssociations', None)
    for key in values:
        rollback['Parameters'].pop(key)
    rollback_path = path.with_suffix('.rollback.json')
    rollback_path.write_text(json.dumps(rollback, indent=2) + '\n')
    rollback_path.chmod(0o600)
    rollback_params = path.with_suffix('.rollback.parameters.json')
    rollback_params.write_text(json.dumps([{'ParameterKey': k, 'UsePreviousValue': True} for k in rollback['Parameters']], indent=2) + '\n')
    rollback_params.chmod(0o600)
    print('Prepared private candidate, parameters and live-baseline rollback files. Review a change set before execution.')


if __name__ == '__main__':
    main()
