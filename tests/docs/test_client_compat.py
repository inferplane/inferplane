import copy
import sys
import tempfile
from unittest.mock import patch
from pathlib import Path
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'scripts'))
import build_client_compat_docs as report


class CompatibilityReportTests(unittest.TestCase):
    def test_public_projection_drops_prompt_error_and_identity(self):
        row = report.project_api({'id': 'r', 'model_id': 'm', 'success': False,
                                  'state': 'failed', 'status': 200,
                                  'error': 'PRIVATE input exceeds the context window',
                                  'output_text': 'PRIVATE', 'principal': 'PRIVATE',
                                  'usage': {'input': 42, 'secret': 'PRIVATE'}})
        self.assertEqual(row['failure_class'], 'context_limit')
        self.assertNotIn('PRIVATE', str(row))
        self.assertEqual(row['usage'], {'input': 42})

    def test_http_200_does_not_override_stream_error(self):
        row = report.project_api({'success': True, 'status': 200,
                                  'error': 'context window exceeded'})
        self.assertFalse(row['success'])
        self.assertEqual(row['failure_class'], 'context_limit')

    def test_filtered_success_is_not_published_as_success(self):
        self.assertEqual(report.failure({'success': True, 'stop_reason': 'content_filtered'}),
                         'content_filtered')

    def test_unknown_cost_and_skipped_calls_are_distinct(self):
        rows = [dict(success=True, invoked=True, estimated_cost_micro_usd=10),
                dict(success=False, invoked=True, estimated_cost_micro_usd=None,
                     failure_class='context_limit'),
                dict(success=False, invoked=False, estimated_cost_micro_usd=None,
                     failure_class='not_invoked')]
        stats = report.stats(rows)
        self.assertEqual((stats['calls'], stats['successes'], stats['known_cost'], stats['unknown']),
                         (2, 1, 10, 1))

    def test_fixture_projection_excludes_fake_price(self):
        row = report.project_probe({'client': 'claude', 'model_id': 'm',
                                    'model_usage': {'m': {'contextWindow': 200000, 'costUSD': 100}}})
        self.assertEqual(row['reported_context_windows'], [200000])
        self.assertNotIn('costUSD', str(row))

    def test_duplicate_probe_cannot_silently_replace_window(self):
        probe = {'model_id': 'm', 'client': 'claude', 'variant': 'default'}
        data = dict(models=[{'id': 'm'}], api_records=[], cli_runs=[], cli_requests=[],
                    offline_probes=[probe, copy.deepcopy(probe)])
        with self.assertRaisesRegex(ValueError, 'duplicate or unknown offline probe'):
            report.validate(data)

    def test_run_cost_must_include_each_invoked_attempt(self):
        data = dict(models=[{'id': 'm'}], api_records=[], offline_probes=[],
                    cli_requests=[{'id': 'r', 'model_id': 'm', 'invoked': True,
                                   'estimated_cost_micro_usd': 10}],
                    cli_runs=[{'run_id': 'x', 'model_id': 'm', 'request_ids': ['r'],
                               'invoked_requests': 1, 'known_cost_micro_usd': 0,
                               'unknown_cost_requests': 0}])
        with self.assertRaisesRegex(ValueError, 'run cost mismatch'):
            report.validate(data)

    def test_null_error_does_not_turn_success_into_upstream_error(self):
        row = report.project_api(dict(success=True, terminal=True, status=200, error=None))
        self.assertTrue(row['success'])
        self.assertFalse(row['response_error'])

    def test_wrong_retrieval_is_not_a_transport_error(self):
        row = report.project_api(dict(success=False, terminal=True, status=200, error=None,
                                      output_characters=30, retrieval_correct=False))
        self.assertEqual(row['failure_class'], 'retrieval_mismatch')
        self.assertFalse(row['response_error'])

    def test_public_input_cannot_smuggle_unknown_fields(self):
        data = dict(models=[{'id': 'm'}], api_records=[], offline_probes=[], cli_runs=[],
                    cli_requests=[{'id': 'r', 'model_id': 'm', 'prompt': 'PRIVATE'}])
        with self.assertRaisesRegex(ValueError, 'unexpected public fields'):
            report.validate(data)

    def test_compaction_table_preserves_unknown_cost(self):
        data = dict(models=[], api_records=[], offline_probes=[], cli_runs=[],
                    compaction_runs=[dict(run_id='test', gateway_build='patched', compact_limit=180000,
                                         initial_input_tokens=200000, compaction_input_tokens=None,
                                         following_input_tokens=None, compaction_completed=False,
                                         turn_statuses=['completed', 'failed'], known_cost_micro_usd=1,
                                         unknown_cost_requests=1)])
        self.assertIn('$0.000001 + ? (1)', report.tables(data)['compaction'])

    def test_empty_table_markers_are_populated_and_then_stable(self):
        data = dict(models=[], api_records=[], offline_probes=[], cli_runs=[], compaction_runs=[])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pages = root / 'docs/performance'
            pages.mkdir(parents=True)
            text = '\n'.join('<!-- compatibility:' + name + ' -->\n<!-- /compatibility:' + name + ' -->'
                             for name in report.tables(data))
            for name in ('clients.md', 'clients.ko.md'):
                (pages / name).write_text(text)
            with patch.object(report, 'ROOT', root):
                changed = report.render(data)
                self.assertEqual(len(changed), 2)
                for path, content in changed:
                    self.assertIn(('| 모델 |' if path.name.endswith('.ko.md') else '| Model |'), content)
                    path.write_text(content)
                self.assertEqual(report.render(data), [])

    def test_client_character_rejection_is_not_an_upstream_error(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            (home / 'stderr.txt').write_text('input_too_large {"max_chars":1048576,"actual_chars":1638627}')
            run = dict(invoked_requests=0, success=False)
            self.assertEqual(report.cli_failure(run, [], home), 'client_input_limit')
            self.assertEqual((run['max_chars'], run['actual_chars']), (1048576, 1638627))

    def test_large_cost_format_keeps_integer_precision(self):
        self.assertEqual(report.money(9007199254740993), '$9,007,199,254.740993')


if __name__ == '__main__':
    unittest.main()
