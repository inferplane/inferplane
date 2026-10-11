import copy
import importlib.util
import json
import tempfile
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('prepare', ROOT / 'scripts/prepare-cloudfront-dashboard.py')
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)

class DashboardDeploymentTests(unittest.TestCase):
    def test_original_yaml_getatt_representation_is_not_normalized(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'fetched.json'
            path.write_text(json.dumps({'TemplateBody': 'Resources:\n  TaskDef:\n    Type: AWS::ECS::TaskDefinition\n    Properties:\n      ExecutionRoleArn: !GetAtt ExecutionRole.Arn\n'}))
            result = prepare.load_template(path)
            self.assertEqual(result['Resources']['TaskDef']['Properties']['ExecutionRoleArn'], {'Fn::GetAtt': 'ExecutionRole.Arn'})

    def setUp(self):
        self.template = {'Parameters': {'Image': {'Type': 'String'}}, 'Resources': {
            'Service': {'Type': 'AWS::ECS::Service', 'Properties': {'DesiredCount': 1}},
            'Distribution': {'Type': 'AWS::CloudFront::Distribution', 'Properties': {'DistributionConfig': {
                'Origins': [{'Id': 'data', 'DomainName': {'Fn::GetAtt': ['Nlb', 'DNSName']}, 'CustomOriginConfig': {'HTTPPort': 8080}}],
                'DefaultCacheBehavior': {'TargetOriginId': 'data', 'CachePolicyId': {'Ref': 'NoCacheForwardAll'}},
                'CacheBehaviors': [{'PathPattern': '/admin/*', 'TargetOriginId': 'admin'}],
            }}},
        }}
        self.live = {'DistributionConfig': {'Origins': {'Items': [{'Id': 'data', 'DomainName': 'origin.invalid', 'VpcOriginConfig': {'VpcOriginId': 'vo_existing', 'OriginReadTimeout': 60}}]},
            'Aliases': {'Items': ['inferplane.example.invalid']}, 'ViewerCertificate': {'CloudFrontDefaultCertificate': False, 'ACMCertificateArn': 'arn:existing', 'SSLSupportMethod': 'sni-only'},
            'DefaultCacheBehavior': {'FunctionAssociations': {'Quantity': 0}, 'LambdaFunctionAssociations': {'Quantity': 0}}}}
        self.outputs = dict(AssetsDomain='private.s3.ap-northeast-2.amazonaws.com', AccessControlId='EEXAMPLE', SecurityHeadersId='1234-abcd',
                            EntryFunctionArn='arn:aws:cloudfront::000000000000:function/dashboard-entry')

    def test_exact_static_paths_and_unmodified_backend(self):
        before = copy.deepcopy(self.template)
        after, params = prepare.prepare(self.template, self.live, self.outputs, 'releases/' + 'a' * 24)
        self.assertEqual(self.template, before)
        self.assertEqual(after['Resources']['Service'], before['Resources']['Service'])
        cfg = after['Resources']['Distribution']['Properties']['DistributionConfig']
        self.assertEqual([b['PathPattern'] for b in cfg['CacheBehaviors'][:4]], list(prepare.PATHS))
        self.assertNotIn('/ui/*', [b['PathPattern'] for b in cfg['CacheBehaviors']])
        self.assertNotIn('/ui/auth/config', [b['PathPattern'] for b in cfg['CacheBehaviors']])
        self.assertEqual(cfg['CacheBehaviors'][4:], before['Resources']['Distribution']['Properties']['DistributionConfig']['CacheBehaviors'])
        self.assertEqual(cfg['DefaultCacheBehavior']['CachePolicyId'], {'Ref': 'NoCacheForwardAll'})
        self.assertEqual(cfg['Origins'][0]['VpcOriginConfig'], {'VpcOriginId': 'vo_existing', 'OriginReadTimeout': 60})
        self.assertNotIn('CustomOriginConfig', cfg['Origins'][0])
        self.assertEqual(cfg['Aliases'], ['inferplane.example.invalid'])
        self.assertEqual(cfg['ViewerCertificate']['AcmCertificateArn'], 'arn:existing')
        self.assertNotIn('CloudFrontDefaultCertificate', cfg['ViewerCertificate'])
        self.assertEqual(cfg['Origins'][0]['DomainName'], {'Fn::GetAtt': ['Nlb', 'DNSName']})
        self.assertEqual(params['DashboardReleasePrefix'], 'releases/' + 'a' * 24)

    def test_refuse_existing_function_and_origin_drift(self):
        self.live['DistributionConfig']['DefaultCacheBehavior']['FunctionAssociations'] = {'Quantity': 1}
        with self.assertRaisesRegex(ValueError, 'edge function'):
            prepare.prepare(self.template, self.live, self.outputs, 'releases/' + 'a' * 24)
        self.live['DistributionConfig']['DefaultCacheBehavior']['FunctionAssociations'] = {'Quantity': 0}
        self.live['DistributionConfig']['Origins']['Items'][0]['Id'] = 'unknown'
        with self.assertRaisesRegex(ValueError, 'origin IDs'):
            prepare.prepare(self.template, self.live, self.outputs, 'releases/' + 'a' * 24)

    def test_invalid_cross_stack_resources_or_prefix_refuse(self):
        for prefix in ('/releases/' + 'a' * 24, 'releases/../../other', 'releases/a'):
            with self.assertRaises(ValueError):
                prepare.prepare(self.template, self.live, self.outputs, prefix)
        self.outputs['AssetsDomain'] = 'https://public.invalid'
        with self.assertRaises(ValueError):
            prepare.prepare(self.template, self.live, self.outputs, 'releases/' + 'a' * 24)

    def test_private_assets_security(self):
        import yaml
        class Loader(yaml.SafeLoader):
            pass
        Loader.add_multi_constructor('!', lambda loader, tag, node: loader.construct_scalar(node) if isinstance(node, yaml.ScalarNode) else loader.construct_sequence(node))
        t = yaml.load((ROOT / 'deploy/cloudfront/dashboard-resources.yaml').read_text(), Loader=Loader)
        bucket = t['Resources']['Assets']
        self.assertEqual(bucket['DeletionPolicy'], 'Retain')
        self.assertTrue(all(bucket['Properties']['PublicAccessBlockConfiguration'].values()))
        self.assertEqual(bucket['Properties']['VersioningConfiguration']['Status'], 'Enabled')
        self.assertEqual(t['Resources']['AccessControl']['Properties']['OriginAccessControlConfig']['SigningBehavior'], 'always')
        source = t['Resources']['EntryRedirect']['Properties']['FunctionCode']
        self.assertIn("request.uri === '/' || request.uri === '/ui'", source)
        self.assertNotIn('authorization', source.lower())

if __name__ == '__main__':
    unittest.main()
