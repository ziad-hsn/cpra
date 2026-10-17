"""Verify offline review signatures over the exact private assessment.

The operator supplies trusted reviewer keys independently of candidate artifacts.
A signature authenticates a statement; it cannot establish that an adequate
review happened. This reader never authorizes publication or signs anything.
"""
import base64
from datetime import datetime, timezone
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

from evidence import decode_report
import release


NAMESPACE = 'cpra-release-review@ziad-hsn.github.io'
MAX_RECORD_BYTES = 1 << 20
MAX_POLICY_BYTES = 64 << 10
MAX_SIGNATURE_BYTES = 16 << 10
MAX_REVIEWERS = 16
PRINCIPAL = re.compile(r'[A-Za-z0-9_.+-]+@[A-Za-z0-9.-]+\Z')


def require(condition, detail):
    if not condition:
        raise ValueError('Invalid independent review evidence: ' + detail)


def content(path, inputs, limit):
    path = Path(path).absolute()
    require(path.resolve(strict=True) == path, 'input contains a link or alias')
    require(path.is_file() and path.stat().st_size <= limit, 'input is not a bounded regular file')
    expected = inputs.remember(path)
    with path.open('rb') as stream:
        data = stream.read(limit + 1)
    require(len(data) <= limit and hashlib.sha256(data).hexdigest() == expected,
            'input changed while being read')
    return data


def principals(values):
    require(isinstance(values, list) and 0 < len(values) <= MAX_REVIEWERS,
            'trusted identities must be a bounded nonempty list')
    result, keys = {}, set()
    for row in values:
        require(isinstance(row, dict) and set(row) == {'principal', 'public_key'}, 'invalid trusted identity')
        principal, key = row['principal'], row['public_key']
        require(isinstance(principal, str) and len(principal) <= 128 and PRINCIPAL.fullmatch(principal),
                'identity must be an explicit principal, without patterns')
        require(isinstance(key, str) and len(key) <= 8192 and '\n' not in key and '\r' not in key,
                'invalid public key')
        parts = key.split(' ')
        require(len(parts) == 2 and re.fullmatch(r'(ssh|ecdsa|sk)-[A-Za-z0-9@._+-]+', parts[0]),
                'expected an OpenSSH public key without options or comments')
        try:
            payload = base64.b64decode(parts[1], validate=True)
        except ValueError:
            raise ValueError('Invalid independent review evidence: invalid public key encoding') from None
        require(16 <= len(payload) <= 4096, 'public key is outside its size bounds')
        fingerprint = hashlib.sha256(payload).hexdigest()
        require(principal not in result and fingerprint not in keys, 'duplicate trusted identity or key')
        result[principal] = (key, fingerprint)
        keys.add(fingerprint)
    return result


def policy(path, inputs, assessment, records):
    path = Path(path).absolute()
    require(not path.is_relative_to(release.ROOT), 'reviewer trust must be supplied outside candidate source')
    require(not any(path.is_relative_to(p.parent) for p in records),
            'reviewer trust cannot come from the review artifact directory')
    value = decode_report(content(path, inputs, MAX_POLICY_BYTES), path.name)
    require(set(value) == {'schema_version', 'repository', 'authors', 'reviewers', 'minimum_reviewers'}
            and type(value['schema_version']) is int and value['schema_version'] == 1
            and value['repository'] == 'ziad-hsn/cpra', 'unsupported reviewer trust policy')
    require(path.stat().st_mode & 0o022 == 0, 'reviewer trust policy is writable by group or other users')
    require(str(path) not in assessment['input_sha256'], 'assessment cannot establish its own reviewer trust')
    authors, reviewers = principals(value['authors']), principals(value['reviewers'])
    require(not authors.keys() & reviewers.keys()
            and not {row[1] for row in authors.values()} & {row[1] for row in reviewers.values()},
            'authors cannot review their own candidate, including under another identity')
    minimum = value['minimum_reviewers']
    require(type(minimum) is int and 1 <= minimum <= len(reviewers), 'invalid minimum reviewer count')
    return reviewers, minimum


def signature(data, signed, principal, public_key, ssh_keygen):
    # Verify captured bytes in an isolated directory; never read a private key or
    # inherit an agent, hardware-provider setting, shell, or candidate executable.
    with tempfile.TemporaryDirectory(prefix='cpra-review-signature-') as temporary:
        root = Path(temporary)
        allowed, proof = root / 'allowed-signers', root / 'review.sig'
        allowed.write_text(principal + ' namespaces="' + NAMESPACE + '" ' + public_key + '\n')
        proof.write_bytes(signed)
        allowed.chmod(0o600)
        proof.chmod(0o600)
        try:
            result = subprocess.run([str(ssh_keygen), '-Y', 'verify', '-f', str(allowed),
                                     '-I', principal, '-n', NAMESPACE, '-s', str(proof)],
                                    input=data, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                    timeout=10, env={'PATH': os.defpath, 'LANG': 'C', 'LC_ALL': 'C'})
        except subprocess.TimeoutExpired:
            raise ValueError('Invalid independent review evidence: signature verification timed out') from None
    require(result.returncode == 0, 'review signature is invalid, untrusted, or uses another namespace')


def verify_reviews(manifest, assessment_path, trust_path, records, components, inputs, *, ssh_keygen=None):
    identity = release.evidence_identity(manifest)
    require(isinstance(records, list) and 0 < len(records) <= MAX_REVIEWERS,
            'missing or excessive review records')
    records = [Path(path).absolute() for path in records]
    require(len(set(records)) == len(records), 'duplicate review record')
    assessment_path = Path(assessment_path).absolute()
    assessment_data = content(assessment_path, inputs, 16 << 20)
    assessment = decode_report(assessment_data, assessment_path.name)
    require(type(assessment.get('schema_version')) is int and assessment['schema_version'] == 1
            and assessment.get('release_identity') == identity
            and assessment.get('scope') == 'private frozen-release evidence assessment'
            and assessment.get('release_gate_passed') is False and assessment.get('publication_locked') is True,
            'review must identify the original locked assessment of this frozen release')
    reviewed = assessment.get('components')
    require(isinstance(reviewed, dict) and set(reviewed) == set(components) | {'independent_review'},
            'assessment omits required component readers')
    require(all(reviewed[name] == result for name, result in components.items()),
            'reviewed component outcomes differ from the current assessment')
    inventory = assessment.get('input_sha256')
    require(isinstance(inventory, dict) and 0 < len(inventory) <= 100000,
            'assessment omits its retained inputs')
    require(str(assessment_path) not in inventory and not any(str(path) in inventory for path in records),
            'review inputs cannot authenticate themselves')
    for name, digest in inventory.items():
        require(isinstance(name, str) and Path(name).is_absolute()
                and isinstance(digest, str) and re.fullmatch('[a-f0-9]{64}', digest),
                'invalid reviewed input fingerprint')
        inputs.remember(Path(name), digest)
    reviewers, minimum = policy(trust_path, inputs, assessment, records)
    executable = Path(ssh_keygen or shutil.which('ssh-keygen') or '')
    require(executable.is_absolute() and executable.is_file(), 'an installed OpenSSH verifier is required')
    executable = executable.resolve(strict=True)
    verifier_sha256 = inputs.remember(executable)
    assessment_sha256 = hashlib.sha256(assessment_data).hexdigest()
    accepted, requested_changes, seen = [], [], set()
    for path in records:
        raw = content(path, inputs, MAX_RECORD_BYTES)
        record = decode_report(raw, path.name)
        require(set(record) == {'schema_version', 'release_identity', 'assessment_sha256', 'principal',
                                'reviewed_at', 'components', 'conclusion', 'notes', 'findings'}
                and type(record['schema_version']) is int and record['schema_version'] == 1,
                'unsupported signed review record')
        principal = record['principal']
        require(isinstance(principal, str) and principal in reviewers and principal not in seen,
                'reviewer is untrusted or submitted duplicate dispositions')
        seen.add(principal)
        require(record['release_identity'] == identity and record['assessment_sha256'] == assessment_sha256,
                'review targets another source or assessment')
        require(isinstance(record['components'], list) and len(record['components']) == len(components)
                and all(isinstance(name, str) for name in record['components'])
                and set(record['components']) == set(components), 'review does not cover every required component')
        require(record['conclusion'] in ('approve', 'changes_requested')
                and isinstance(record['notes'], str) and 1 <= len(record['notes'].strip()) <= 8192,
                'review omits its decision or substantive notes')
        try:
            at = datetime.fromisoformat(record['reviewed_at'].replace('Z', '+00:00'))
        except (AttributeError, TypeError, ValueError):
            raise ValueError('Invalid independent review evidence: invalid review timestamp') from None
        require(at.tzinfo is not None and manifest['source_date_epoch'] <= at.timestamp()
                <= datetime.now(timezone.utc).timestamp() + 300, 'review timestamp predates source or is in the future')
        findings = record['findings']
        require(isinstance(findings, list) and len(findings) <= 256, 'invalid review findings')
        finding_ids, unresolved = set(), False
        for finding in findings:
            require(isinstance(finding, dict) and set(finding) == {'id', 'component', 'status', 'notes', 'evidence'}
                    and isinstance(finding['id'], str) and 1 <= len(finding['id']) <= 128
                    and finding['id'] not in finding_ids and isinstance(finding['component'], str)
                    and finding['component'] in components
                    and finding['status'] in ('open', 'resolved')
                    and isinstance(finding['notes'], str) and 1 <= len(finding['notes'].strip()) <= 8192,
                    'invalid, duplicate, or unscoped review finding')
            finding_ids.add(finding['id'])
            proof = finding['evidence']
            require(isinstance(proof, dict) and 0 < len(proof) <= 32
                    and all(name in inventory and inventory[name] == value for name, value in proof.items()),
                    'finding resolution must reference original reviewed evidence')
            unresolved |= finding['status'] == 'open'
        require(not unresolved or record['conclusion'] == 'changes_requested',
                'an approval cannot leave unresolved findings')
        signed = content(path.with_name(path.name + '.sig'), inputs, MAX_SIGNATURE_BYTES)
        signature(raw, signed, principal, reviewers[principal][0], executable)
        (accepted if record['conclusion'] == 'approve' else requested_changes).append(principal)
    ready = (len(accepted) >= minimum and not requested_changes
             and all(row.get('status') == 'consistent_pending_review' for row in components.values()))
    inputs.unchanged()
    return {'schema_version': 1, 'status': 'consistent_pending_review' if ready else 'incomplete',
            'release_identity': identity, 'release_gate_passed': False, 'publication_locked': True,
            'assessment_sha256': assessment_sha256, 'signature_namespace': NAMESPACE,
            'approved_reviewers': accepted, 'changes_requested_by': requested_changes,
            'minimum_reviewers': minimum, 'verifier_sha256': verifier_sha256,
            'trust_scope': 'operator supplied identities; author list and key ownership require out-of-band verification',
            'input_sha256': dict(inputs.files),
            'remaining': [] if ready else ['Complete all component evidence and resolve review findings.']}
