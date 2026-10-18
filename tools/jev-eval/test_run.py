import io
import json
from pathlib import Path
import tempfile
import unittest
from prepare_review import MODEL, prepare
from run import NoRedirect, execute
from report import summarize


class Response(io.BytesIO):
    status = 200


class MockProvider:
    def __init__(self, fail=False):
        self.calls = 0
        self.fail = fail

    def open(self, request, timeout):
        self.calls += 1
        if self.fail:
            raise RuntimeError('SECRET_DO_NOT_RECORD provider reflected prompt')
        assert timeout == 15
        payload = json.loads(request.data)
        return Response(json.dumps({'model': MODEL, 'answers': {
            c: {'type': 'noul', 'noul': .1} for c in payload['questions']
        }}).encode())


class RunnerTests(unittest.TestCase):
    def test_mock_end_to_end_and_no_invented_usage(self):
        with tempfile.TemporaryDirectory() as folder:
            plan = prepare(Path(folder) / 'packet')
            provider = MockProvider()
            result = execute(plan, 'TEST_ONLY', Path(folder) / 'results.json', provider)
            self.assertEqual(provider.calls, 108)
            self.assertEqual(summarize(result)['completed_samples'], 108)
            self.assertFalse(result['usage_complete'])
            with self.assertRaises(FileExistsError):
                execute(plan, 'TEST_ONLY', Path(folder) / 'results.json', provider)
            self.assertEqual(provider.calls, 108)

    def test_failures_never_retry_or_leak_errors(self):
        with tempfile.TemporaryDirectory() as folder:
            plan = prepare(Path(folder) / 'packet')
            provider = MockProvider(fail=True)
            output = Path(folder) / 'results.json'
            result = execute(plan, 'TEST_ONLY', output, provider)
            self.assertEqual(provider.calls, 108)
            self.assertEqual(summarize(result)['failed_samples'], 108)
            self.assertNotIn('SECRET_DO_NOT_RECORD', output.read_text())
            self.assertNotIn('TEST_ONLY', output.read_text())

    def test_changed_plan_rejected_before_call(self):
        with tempfile.TemporaryDirectory() as folder:
            plan = prepare(Path(folder) / 'packet')
            plan['endpoint'] = 'https://example.invalid'
            provider = MockProvider()
            with self.assertRaises(ValueError):
                execute(plan, 'TEST_ONLY', Path(folder) / 'results.json', provider)
            self.assertEqual(provider.calls, 0)

    def test_redirects_are_not_followed(self):
        self.assertIsNone(NoRedirect().redirect_request(None, None, 307, '', {}, 'https://example.invalid'))

    def test_invalid_provider_response_is_failed_not_safe(self):
        class InvalidProvider:
            def open(self, request, timeout):
                return Response(b'{"model":"jev-1.13.0","answers":{}}')
        with tempfile.TemporaryDirectory() as folder:
            plan = prepare(Path(folder) / 'packet')
            result = execute(plan, 'TEST_ONLY', Path(folder) / 'results.json', InvalidProvider())
            self.assertEqual(summarize(result)['failed_samples'], 108)
            self.assertTrue(all('scores' not in row for row in result['results']))

    def test_language_profile_runs_exactly_32_and_cannot_mix_results(self):
        with tempfile.TemporaryDirectory() as folder:
            plan = prepare(Path(folder) / 'packet', 'language-32')
            provider = MockProvider()
            result = execute(plan, 'TEST_ONLY', Path(folder) / 'results.json', provider)
            self.assertEqual(provider.calls, 32)
            report = summarize(result)
            self.assertEqual(report['expected_samples'], 32)
            self.assertEqual(report['scenario_families'], 16)
            self.assertFalse(report['independent_acceptance'])
            result['profile'] = 'expanded-108'
            with self.assertRaises(ValueError):
                summarize(result)

    def test_profile_switch_or_call_limit_edit_rejected_before_network(self):
        for field, value in [('profile', 'expanded-108'), ('profile', 'unknown'),
                             ('proposed_requests', 108)]:
            with self.subTest(field=field), tempfile.TemporaryDirectory() as folder:
                plan = prepare(Path(folder) / 'packet', 'language-32')
                plan[field] = value
                provider = MockProvider()
                with self.assertRaises(ValueError):
                    execute(plan, 'TEST_ONLY', Path(folder) / 'results.json', provider)
                self.assertEqual(provider.calls, 0)
