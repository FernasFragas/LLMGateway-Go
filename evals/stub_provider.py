#!/usr/bin/env python3
"""A fake OpenAI-compatible provider for the end-to-end check.

It answers each evaluation prompt with the answer its case expects, so a run
against it passes only if every hop between the runner and here works: the
ServiceAccount token, gateway auth, routing, provider-key delivery (a request
without the expected key gets 401), the openai adapter's translation both
ways, and the runner's scoring. It tests the plumbing, never a model.
"""
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

NO_ANSWER = 'STUB-NO-ANSWER'  # a prompt with no case fails its check visibly


def answers_from(cases):
    """Map each case's prompt to the text that satisfies its check."""
    table = {}
    for case in cases:
        kind, expected = next(iter(case['check'].items()))
        table[case['prompt']] = json.dumps(expected) if kind == 'json_equal' else expected
    return table


def make_handler(answers, key):
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            self._send(200 if self.path == '/healthz' else 404, {})

        def do_POST(self):
            if self.path != '/v1/chat/completions':
                return self._send(404, {'error': {'message': 'not found'}})
            if self.headers.get('Authorization') != 'Bearer ' + key:
                return self._send(401, {'error': {'message': 'invalid provider key'}})
            body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))))
            prompt = next((m.get('content', '') for m in reversed(body.get('messages', []))
                           if m.get('role') == 'user'), '')
            answer = answers.get(prompt, NO_ANSWER)
            prompt_tokens = max(1, len(prompt.split()))
            completion_tokens = max(1, len(answer.split()))
            self._send(200, {
                'id': 'stub-completion', 'object': 'chat.completion', 'model': body.get('model', ''),
                'choices': [{'index': 0, 'finish_reason': 'stop',
                             'message': {'role': 'assistant', 'content': answer}}],
                'usage': {'prompt_tokens': prompt_tokens, 'completion_tokens': completion_tokens,
                          'total_tokens': prompt_tokens + completion_tokens},
            })

        def _send(self, status, payload):
            data = json.dumps(payload).encode()
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, fmt, *args):
            sys.stderr.write('stub: ' + fmt % args + '\n')  # never logs headers, so never the key

    return Handler


def main():
    cases = json.loads(Path(os.environ.get('STUB_CASES', 'cases.json')).read_text())
    port = int(os.environ.get('STUB_PORT', '8080'))
    server = ThreadingHTTPServer(('', port), make_handler(answers_from(cases), os.environ['STUB_KEY']))
    server.serve_forever()


if __name__ == '__main__':
    main()
