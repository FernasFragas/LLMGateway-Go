import json
import threading
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

import stub_provider

CASES = [
    {'id': 'word', 'prompt': 'Say READY', 'check': {'exact': 'READY'}},
    {'id': 'obj', 'prompt': 'Give JSON', 'check': {'json_equal': {'even': True}}},
]


class StubProviderTest(unittest.TestCase):
    def setUp(self):
        handler = stub_provider.make_handler(stub_provider.answers_from(CASES), 'k')
        self.server = ThreadingHTTPServer(('127.0.0.1', 0), handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.base = f'http://127.0.0.1:{self.server.server_address[1]}'

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()

    def chat(self, prompt, key='k'):
        req = urllib.request.Request(
            self.base + '/v1/chat/completions',
            data=json.dumps({'model': 'm', 'messages': [{'role': 'user', 'content': prompt}]}).encode(),
            headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(req) as resp:
                return resp.status, json.loads(resp.read())
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read())

    def test_a_known_prompt_gets_the_answer_its_check_expects(self):
        status, body = self.chat('Say READY')
        self.assertEqual(status, 200)
        self.assertEqual(body['choices'][0]['message']['content'], 'READY')
        self.assertEqual(body['choices'][0]['finish_reason'], 'stop')

    def test_a_json_check_is_answered_with_json(self):
        _, body = self.chat('Give JSON')
        self.assertEqual(json.loads(body['choices'][0]['message']['content']), {'even': True})

    def test_usage_adds_up(self):
        _, body = self.chat('Say READY')
        usage = body['usage']
        self.assertEqual(usage['prompt_tokens'] + usage['completion_tokens'], usage['total_tokens'])

    def test_a_wrong_provider_key_is_refused(self):
        status, _ = self.chat('Say READY', key='wrong')
        self.assertEqual(status, 401)

    def test_an_unknown_prompt_fails_visibly(self):
        _, body = self.chat('something else')
        self.assertEqual(body['choices'][0]['message']['content'], stub_provider.NO_ANSWER)


if __name__ == '__main__':
    unittest.main()
