#!/usr/bin/env python3
"""Measure local Ollama binary reranking on fixed dev text examples; no API quality claim."""
import argparse
import hashlib
import importlib.util
import json
import math
import time
import urllib.request
from pathlib import Path

SCRIPT = Path(__file__).resolve()
spec = importlib.util.spec_from_file_location('rerank_probe', SCRIPT.with_name('evaluate-reranker-preflight.py'))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)
BASE = 'http://127.0.0.1:11434'
LOCAL_HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def request(path, body=None):
    req = urllib.request.Request(BASE + path, data=None if body is None else json.dumps(body).encode(),
                                 headers={'Content-Type': 'application/json'})
    with LOCAL_HTTP.open(req, timeout=120) as response:
        raw = response.read((2 << 20) + 1)
        if len(raw) > 2 << 20:
            raise ValueError('oversized provider response')
        return json.loads(raw)


def digest(model):
    matches = [m for m in request('/api/tags')['models'] if m['name'] == model]
    if len(matches) != 1 or len(matches[0]['digest']) != 64:
        raise ValueError('model identity unavailable')
    return matches[0]['digest']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--model', default='qwen3-vl:4b')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if not args.output.is_absolute():
        parser.error('output must be a new absolute path')
    raw = probe.MANIFEST.read_bytes()
    dataset = json.loads(raw)
    documents = {d['id']: d['text'] for d in dataset['documents'] if 'text' in d}
    chosen = ['invoice-receipt', 'contract-purchase', 'laptop-review', 'contract-service', 'meeting-notes', 'config-production']
    queries = [q for q in dataset['queries'] if q['id'] in ('invoice-1', 'invoice-2', 'contracts-1')]
    model_digest = digest(args.model)
    report = {'completed': False, 'model': args.model, 'model_digest': model_digest,
              'ollama_version': request('/api/version')['version'], 'manifest_sha256': hashlib.sha256(raw).hexdigest(),
              'script_sha256': probe.digest(SCRIPT), 'prompt_script_sha256': probe.digest(Path(probe.__file__)),
              'limitations': ['Only 18 synthetic development query/document pairs.',
                              'No heldout queries, API/ACL, OCR, or whole-corpus quality claims.',
                              'Binary model score is not a calibrated relevance probability.'], 'pairs': []}
    with args.output.open('x', encoding='utf-8') as output:
        def save():
            output.seek(0)
            json.dump(report, output, ensure_ascii=False, indent=2, allow_nan=False)
            output.write('\n'); output.truncate(); output.flush()
        save()
        try:
            for q in queries:
                question = q['question'].replace('上个月上传的', '')
                for doc in chosen:
                    body = {'model': args.model, 'prompt': probe.prompt(question, documents[doc]), 'raw': True,
                            'stream': False, 'think': False, 'truncate': False, 'logprobs': True, 'top_logprobs': 20,
                            'keep_alive': '1m', 'options': {'num_predict': 1, 'temperature': 0, 'num_ctx': 16384,
                                                         'top_k': 0, 'top_p': 1, 'min_p': 0}}
                    started = time.monotonic()
                    response = request('/api/generate', body)
                    pair = {'query_id': q['id'], 'document': doc, 'expected_relevant': doc in q['relevant'],
                            'seconds': time.monotonic() - started, 'response': response.get('response'),
                            'done_reason': response.get('done_reason'), 'logprobs': response.get('logprobs')}
                    lp = response.get('logprobs') or []
                    if response.get('model') != args.model or not response.get('done') or len(lp) != 1:
                        raise ValueError('unexpected single-token response')
                    choices = lp[0].get('top_logprobs') or []
                    scores = {x['token']: x['logprob'] for x in choices}
                    if all(k in scores and math.isfinite(scores[k]) for k in ('yes', 'no')):
                        difference = scores['no'] - scores['yes']
                        e = math.exp(-abs(difference))
                        pair['score'] = e / (1 + e) if difference >= 0 else 1 / (1 + e)
                    else:
                        pair['unscored'] = 'canonical_yes_or_no_missing'
                    report['pairs'].append(pair); save()
                    print(q['id'], doc, pair.get('score', pair.get('unscored')), round(pair['seconds'], 3), flush=True)
            if digest(args.model) != model_digest:
                raise ValueError('model changed during probe')
            report['completed'] = True
        except Exception as error:
            report['error_type'] = type(error).__name__
            raise
        finally:
            save()


if __name__ == '__main__':
    main()
