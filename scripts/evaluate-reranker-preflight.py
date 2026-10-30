#!/usr/bin/env python3
"""Real Qwen3 reranker preflight on the fixed development text subset.

Requires local torch/transformers and the pinned public Safetensors snapshot.
Does not download code, contact production, call cloud inference, or use heldout
queries. This is model feasibility evidence, not the application's API benchmark.
"""
import argparse
import hashlib
import json
import math
import platform
import time
from pathlib import Path

REVISION = 'e61197ed45024b0ed8a2d74b80b4d909f1255473'
WEIGHTS_SHA256 = '27cd75a405b9c1b46b59abfd88aaa209e6fed2a1972cde9b70e7659537c5e65b'
MODEL = 'Qwen/Qwen3-Reranker-0.6B'
ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / 'cloudnest_go/integration/testdata/retrieval/dataset-v1.json'
TASK = 'Given a request to find files, retrieve file contents that satisfy the request.'
# Qwen's documented role layout and empty thinking suffix, with an explicit
# relevance-only instruction. No file text is treated as application authority.
PREFIX = '<|im_start|>system\nDecide whether the Document satisfies the Query under the Instruct. Reply only "yes" or "no".<|im_end|>\n<|im_start|>user\n'
SUFFIX = '<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n'


def digest(path):
    with path.open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def prompt(query, document):
    # Keep tokenizer control tokens from source text out of the role template.
    escape = lambda text: text.replace('<|', '< ')
    return PREFIX + f'<Instruct>: {TASK}\n<Query>: {escape(query)}\n<Document>: {escape(document)}' + SUFFIX


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--model-dir', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--device', choices=('cpu', 'mps'), default='mps')
    parser.add_argument('--dataset', choices=('v1', 'stress-v2'), default='v1')
    args = parser.parse_args()
    if not args.output.is_absolute() or not args.model_dir.is_absolute():
        parser.error('model and new output paths must be absolute')
    weights = args.model_dir / 'model.safetensors'
    if digest(weights) != WEIGHTS_SHA256:
        parser.error('weights do not match the pinned official revision')
    manifest = MANIFEST.with_name('dataset-' + args.dataset + '.json')
    raw = manifest.read_bytes()
    dataset = json.loads(raw)
    documents = [d for d in dataset['documents'] if d.get('text') and (d['owner'] == 'alice' or d.get('share_read'))]
    filenames = {d['id']: f"entry-{i+1:02d}.{d['extension']}" for i, d in enumerate(dataset['documents'])}
    ids = {d['id'] for d in documents}
    queries = [q for q in dataset['queries'] if q['split'] == 'dev' and (not q['relevant'] or set(q['relevant']) & ids)]
    report = {
        'completed': False, 'model': MODEL, 'revision': REVISION, 'dataset_name': args.dataset,
        'weights_sha256': WEIGHTS_SHA256, 'manifest_sha256': hashlib.sha256(raw).hexdigest(),
        'script_sha256': digest(Path(__file__)), 'platform': platform.system() + '/' + platform.machine(),
        'device': args.device, 'document_count': len(documents), 'query_count': len(queries),
        'model_file_sha256': {name: digest(args.model_dir / name) for name in
                             ('config.json', 'generation_config.json', 'tokenizer.json',
                              'tokenizer_config.json', 'vocab.json', 'merges.txt')},
        'limitations': [
            'Reduced text-only development subset; not the full API/visual/ACL benchmark.',
            'No heldout queries used; no corpus labels changed.',
            'Binary model score is not a calibrated probability of real-world relevance.',
            'A single local run is not a latency or resource SLA.',
            'Known previous-month/config-extension fixture filters are applied before scoring; this is not SQL execution.',
        ], 'queries': [],
    }
    # Reserve the path before model execution; retain a partial report on errors.
    with args.output.open('x', encoding='utf-8') as output:
        def save():
            output.seek(0)
            json.dump(report, output, ensure_ascii=False, indent=2, allow_nan=False)
            output.write('\n')
            output.truncate()
            output.flush()
        save()
        try:
            import torch
            import transformers
            from transformers import AutoModelForCausalLM, AutoTokenizer
            report['torch_version'] = torch.__version__
            report['transformers_version'] = transformers.__version__
            if args.device == 'mps' and not torch.backends.mps.is_available():
                raise RuntimeError('MPS unavailable; choose cpu explicitly')
            torch.set_num_threads(4)
            torch.manual_seed(0)
            started = time.monotonic()
            tokenizer = AutoTokenizer.from_pretrained(args.model_dir, padding_side='left', local_files_only=True, trust_remote_code=False)
            model = AutoModelForCausalLM.from_pretrained(args.model_dir, dtype=torch.float16 if args.device == 'mps' else torch.float32,
                local_files_only=True, trust_remote_code=False, use_safetensors=True, attn_implementation='sdpa').to(args.device).eval()
            report['load_seconds'] = time.monotonic() - started
            yes = tokenizer.encode('yes', add_special_tokens=False)
            no = tokenizer.encode('no', add_special_tokens=False)
            if len(yes) != 1 or len(no) != 1 or yes == no:
                raise RuntimeError('unexpected yes/no tokenizer contract')
            report['token_ids'] = {'yes': yes[0], 'no': no[0]}
            for q in queries:
                # The application's SQL enforces calendar constraints. This
                # preflight tests content classification, not date parsing.
                question = q['question']
                candidates = documents
                filters = []
                if '上个月' in question or '上月' in question:
                    candidates = [d for d in candidates if d['uploaded'] == 'previous_month']
                    question = question.replace('上个月', ' ').replace('上月', ' ').replace('上传的', ' ').strip()
                    filters.append('previous_month_upload')
                if '配置文件' in question or '设置文件' in question:
                    extensions = {'json', 'xml', 'yaml', 'yml', 'toml', 'ini', 'conf', 'config', 'env', 'properties', 'txt'}
                    candidates = [d for d in candidates if d['extension'] in extensions]
                    filters.append('configuration_extensions')
                result = {'id': q['id'], 'query': question, 'fixture_filters': filters, 'candidate_count': len(candidates),
                          'relevant_text_ids': [i for i in q['relevant'] if i in ids], 'results': [], 'batch_seconds': []}
                for start in range(0, len(candidates), 4):
                    batch = candidates[start:start + 4]
                    texts = ['Filename: ' + filenames[d['id']] + '\nContent: ' + d['text'].strip() for d in batch]
                    inputs = tokenizer([prompt(question, text) for text in texts], padding=True, truncation=False, return_tensors='pt')
                    if inputs['input_ids'].shape[1] > 2048:
                        raise RuntimeError('input exceeds preflight token budget; refusing silent truncation')
                    inputs = {k: v.to(args.device) for k, v in inputs.items()}
                    began = time.monotonic()
                    with torch.inference_mode():
                        # Last-token-only logits avoid allocating a vocabulary
                        # distribution for every input token in the batch.
                        logits = model(**inputs, logits_to_keep=1).logits[:, -1, :]
                        selected = logits[:, [no[0], yes[0]]].float()
                        scores = torch.softmax(selected, dim=1)[:, 1].cpu().tolist()
                    result['batch_seconds'].append(time.monotonic() - began)
                    for doc, score in zip(batch, scores, strict=True):
                        if not math.isfinite(score) or not 0 <= score <= 1:
                            raise RuntimeError('non-finite or out-of-range model score')
                        result['results'].append({'document': doc['id'], 'score': score, 'relevant': doc['id'] in result['relevant_text_ids']})
                result['results'].sort(key=lambda item: (-item['score'], item['document']))
                report['queries'].append(result)
                save()
                print(q['id'], [(h['document'], round(h['score'], 4)) for h in result['results'][:5]], flush=True)
            report['completed'] = True
        except Exception as error:
            report['error_type'] = type(error).__name__
            raise
        finally:
            save()


if __name__ == '__main__':
    main()
