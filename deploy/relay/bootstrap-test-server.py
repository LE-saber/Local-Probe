#!/usr/bin/env python3
"""Deploy only Local-Probe's R12 test namespace; never print secret contents.

Upload locally built Linux binaries beside this script before running as root.
Does not create accounts, install packages, or change firewall/other services.
"""
import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import socket
import subprocess

ROOT = Path('/opt/local-probe-relay-r12')
CONFIG = Path('/etc/local-probe-relay-r12')
MARKER = 'Managed by Local-Probe R12 public relay test'


def command(args):
    result = subprocess.run(args, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError('deployment command failed: ' + args[0])
    return result.stdout


def write_new(path, data, mode):
    with open(path, 'x', encoding='utf-8') as stream:
        os.chmod(path, mode)
        stream.write(data)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--host', required=True)
    parser.add_argument('--port', type=int, default=8443)
    parser.add_argument('--bind-address', default='', help='specific local IP; avoid wildcard listeners on shared Docker hosts')
    parser.add_argument('--tcp-max-segment', type=int, default=0, help='optional per-socket Linux TCP MSS; does not change routes/firewall')
    parser.add_argument('--relay-sha256', required=True)
    parser.add_argument('--edge-sha256', required=True)
    parser.add_argument('--update-owned-units', action='store_true')
    args = parser.parse_args()
    if os.geteuid() != 0 or not re.fullmatch(r'[a-z0-9][a-z0-9.-]{0,251}', args.host):
        raise RuntimeError('root and a constrained DNS hostname required')
    if args.port < 1024 or args.port > 65535:
        raise RuntimeError('unprivileged dedicated TLS port required')
    bind = str(ipaddress.ip_address(args.bind_address)) if args.bind_address else ''
    listen = ('[' + bind + ']' if ':' in bind else bind) + ':' + str(args.port)
    if args.tcp_max_segment != 0 and not 536 <= args.tcp_max_segment <= 1460:
        raise RuntimeError('TCP MSS must be 0 or between 536 and 1460')
    pwd.getpwnam('nobody')
    for name, expected in [('local-probe-relay', args.relay_sha256), ('local-probe-relay-edge', args.edge_sha256)]:
        binary = ROOT / name
        if not re.fullmatch(r'[0-9a-fA-F]{64}', expected) or hashlib.sha256(binary.read_bytes()).hexdigest() != expected.lower():
            raise RuntimeError('uploaded binary checksum mismatch')
        os.chmod(binary, 0o755)

    if CONFIG.exists() and not (CONFIG / 'managed-marker').exists():
        raise RuntimeError('refusing to reuse an unowned configuration directory')
    CONFIG.mkdir(mode=0o700, exist_ok=True)
    if not (CONFIG / 'managed-marker').exists():
        write_new(CONFIG / 'managed-marker', MARKER + '\n', 0o600)
    elif (CONFIG / 'managed-marker').read_text().strip() != MARKER:
        raise RuntimeError('configuration namespace ownership mismatch')

    credentials = CONFIG / 'credentials.json'
    if not credentials.exists():
        values = {'device_id': 'public-test-route', 'target_token': secrets.token_urlsafe(48), 'agent_token': secrets.token_urlsafe(48)}
        write_new(credentials, json.dumps(values), 0o600)
    values = json.loads(credentials.read_text())
    if not (CONFIG / 'relay.env').exists():
        write_new(CONFIG / 'relay.env', 'LOCAL_PROBE_RELAY_TARGET_TOKEN=' + values['target_token'] + '\nLOCAL_PROBE_RELAY_AGENT_TOKEN=' + values['agent_token'] + '\n', 0o600)
    if not (CONFIG / 'relay.json').exists():
        config = {'version': 'local-probe.relay.v1', 'routes': [{'device_id': values['device_id'], 'target_token_env': 'LOCAL_PROBE_RELAY_TARGET_TOKEN', 'agent_token_env': 'LOCAL_PROBE_RELAY_AGENT_TOKEN'}]}
        write_new(CONFIG / 'relay.json', json.dumps(config), 0o600)

    ca, ca_key = CONFIG / 'outer-ca.pem', CONFIG / 'outer-ca.key'
    cert, key = CONFIG / 'edge-cert.pem', CONFIG / 'edge.key'
    if not ca.exists():
        if ca_key.exists():
            raise RuntimeError('incomplete CA initialization; inspect before retrying')
        command(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-sha256', '-days', '30', '-subj', '/CN=Local-Probe-R12-Private-Test-CA', '-keyout', str(ca_key), '-out', str(ca)])
        os.chmod(ca_key, 0o600)
    if not cert.exists():
        if key.exists():
            raise RuntimeError('incomplete edge key initialization; inspect before retrying')
        csr = CONFIG / 'edge.csr'
        command(['openssl', 'req', '-new', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-subj', '/CN=' + args.host, '-keyout', str(key), '-out', str(csr)])
        extension = CONFIG / 'edge-extension.conf'
        write_new(extension, 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:' + args.host + '\n', 0o600)
        command(['openssl', 'x509', '-req', '-in', str(csr), '-CA', str(ca), '-CAkey', str(ca_key), '-CAcreateserial', '-out', str(cert), '-days', '14', '-sha256', '-extfile', str(extension)])
        os.chmod(key, 0o600)

    # PID 1 loads root-only credentials into each service's private credential
    # directory. Reuse nobody; no useradd/DynamicUser/account provisioning.
    changed_units = []
    for name in ['relay', 'relay-edge']:
        unit = Path('/etc/systemd/system/local-probe-' + name + '-r12.service')
        if unit.exists() and MARKER not in unit.read_text():
            raise RuntimeError('refusing to replace an unowned unit')
        if name == 'relay':
            specific = 'EnvironmentFile=' + str(CONFIG / 'relay.env') + '\nLoadCredential=relay.json:' + str(CONFIG / 'relay.json') + '\nExecStart=' + str(ROOT / 'local-probe-relay') + ' -config %d/relay.json -listen 127.0.0.1:8792\n'
        else:
            specific = 'LoadCredential=edge-cert.pem:' + str(cert) + '\nLoadCredential=edge.key:' + str(key) + '\nExecStart=' + str(ROOT / 'local-probe-relay-edge') + ' -listen ' + listen + ' -tcp-max-segment ' + str(args.tcp_max_segment) + ' -upstream http://127.0.0.1:8792 -cert-file %d/edge-cert.pem -key-file %d/edge.key\n'
        content = '# ' + MARKER + '\n[Unit]\nDescription=Local-Probe R12 ' + name + ' test\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nUser=nobody\nGroup=nogroup\n' + specific + 'Restart=on-failure\nRestartSec=2\nNoNewPrivileges=yes\nProtectSystem=strict\nProtectHome=yes\nPrivateTmp=yes\nUMask=0077\nLimitNOFILE=1024\nMemoryMax=192M\nTasksMax=64\nLogRateLimitIntervalSec=30s\nLogRateLimitBurst=20\n\n[Install]\nWantedBy=multi-user.target\n'
        if not unit.exists():
            write_new(unit, content, 0o644)
        elif unit.read_text() != content:
            if not args.update_owned_units:
                raise RuntimeError('existing owned unit differs; review before changing')
            backup = CONFIG / (unit.name + '.before-' + secrets.token_hex(4))
            write_new(backup, unit.read_text(), 0o600)
            unit.write_text(content)
            changed_units.append(unit.name)
    command(['systemctl', 'daemon-reload'])
    command(['systemctl', 'enable', '--now', 'local-probe-relay-r12.service', 'local-probe-relay-edge-r12.service'])
    for unit in changed_units:
        command(['systemctl', 'restart', unit])
    result = {'host': args.host, 'port': args.port, 'outer_ca_pem_sha256': hashlib.sha256(ca.read_bytes()).hexdigest()}
    print(json.dumps(result))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Never include subprocess output, environment values or secret records.
        print('deployment failed: ' + str(error), file=__import__('sys').stderr)
        raise SystemExit(1)
