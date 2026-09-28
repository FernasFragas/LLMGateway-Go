import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

import run


class Handler(BaseHTTPRequestHandler):
    mode = 'pass'
    seen = []

    def log_message(self, *args):
        pass

    def do_GET(self):
        self.send_response(200 if self.path == '/readyz' else 404)
        self.end_headers()

    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        if self.headers.get('Authorization') != 'Bearer test-token':
            self.send_response(401)
            self.end_headers()
            return
        Handler.seen.append((self.path, payload))
        self.send_response(503 if self.mode == 'unavailable' else 200)
        self.end_headers()
        body = {'model': payload['model'], 'choices': [{'message': {'role': 'assistant', 'content': '42'}, 'finish_reason': 'stop'}],
                'usage': {'prompt_tokens': 2, 'completion_tokens': 1, 'total_tokens': 3}}
        if self.mode == 'wrong':
            body['choices'][0]['message']['content'] = '41'
        if self.mode == 'substitute':
            body['model'] = 'different-model'
        if self.mode == 'truncated':
            body['choices'][0]['finish_reason'] = 'length'
        if self.mode == 'bad_usage':
            body['usage']['total_tokens'] = -1
        if self.mode == 'bad_json':
            self.wfile.write(b'not JSON')
        else:
            self.wfile.write(json.dumps(body).encode())


class EvaluationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.base = 'http://127.0.0.1:' + str(cls.server.server_port)

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()

    def invoke(self, mode='pass', token='test-token'):
        Handler.mode, Handler.seen = mode, []
        with tempfile.TemporaryDirectory() as folder:
            cases, output = Path(folder) / 'cases.json', Path(folder) / 'report.json'
            cases.write_text(json.dumps([{'id': 'math', 'prompt': '17+25?', 'check': {'exact': '42'}}]))
            with patch.dict(os.environ, {'GATEWAY_TOKEN': token}), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                code = run.main(['--base-url', self.base, '--model', 'test-model', '--cases', str(cases), '--output', str(output)])
            report = json.loads(output.read_text())
            self.assertNotIn('test-token', output.read_text())
            return code, report

    def test_success_sends_real_gateway_contract_and_records_usage(self):
        code, report = self.invoke()
        self.assertEqual(code, 0)
        self.assertEqual(report['summary']['passed'], 1)
        self.assertEqual(report['summary']['total_tokens'], 3)
        self.assertEqual(report['run']['target'], self.base)
        self.assertEqual(len(report['run']['cases_sha256']), 64)
        path, payload = Handler.seen[0]
        self.assertEqual(path, '/v1/chat')
        self.assertEqual(payload['max_tokens'], 128)
        self.assertEqual(payload['temperature'], 0)

    def test_failures_exit_nonzero_and_record_case(self):
        for mode in ('wrong', 'substitute', 'truncated', 'bad_usage', 'bad_json', 'unavailable'):
            with self.subTest(mode=mode):
                code, report = self.invoke(mode)
                self.assertEqual(code, 1)
                self.assertFalse(report['cases'][0]['passed'])
                self.assertIn('failure', report['cases'][0])

    def test_missing_token_is_setup_error(self):
        code, report = self.invoke(token='')
        self.assertEqual(code, 2)
        self.assertIn('GATEWAY_TOKEN', report['error'])
        self.assertEqual(Handler.seen, [])

    def test_shipped_suite_has_ten_valid_cases(self):
        self.assertEqual(len(run.load_cases(Path(__file__).with_name('cases.json'))), 10)

    def test_empty_suite_is_not_a_vacuous_pass(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'empty.json'
            path.write_text('[]')
            with self.assertRaises(ValueError):
                run.load_cases(path)

    def test_json_boolean_is_not_a_number(self):
        body = {'model': 'm', 'choices': [{'message': {'role': 'assistant', 'content': '{"even":1}'}, 'finish_reason': 'stop'}],
                'usage': {'prompt_tokens': 2, 'completion_tokens': 1, 'total_tokens': 3}}
        self.assertFalse(run.score(body, {'check': {'json_equal': {'even': True}}}, 'm')[0])

    def test_json_and_contains_criteria(self):
        body = {'model': 'm', 'choices': [{'message': {'role': 'assistant', 'content': '{"city":"Lisbon"}'}, 'finish_reason': 'stop'}],
                'usage': {'prompt_tokens': 2, 'completion_tokens': 1, 'total_tokens': 3}}
        for criterion in ({'json_equal': {'city': 'Lisbon'}}, {'contains': 'LISBON'}):
            self.assertTrue(run.score(body, {'check': criterion}, 'm')[0])


if __name__ == '__main__':
    unittest.main()
