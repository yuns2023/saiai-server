#!/usr/bin/env python3
"""Official Codex -> TLS capture -> actual SAIAI proxy -> mock Gateway proof.

Only loopback sockets and synthetic credentials are used. Captured bodies stay
in memory; the output contains counts, digests and comparison results. The
capture proxy refuses every destination except the two managed OpenAI hosts.
It never resolves those hosts or opens an Internet connection.
"""
from __future__ import annotations

import argparse
import base64
import datetime as dt
import hashlib
import json
import os
import queue
import signal
import socket
import socketserver
import ssl
import struct
import subprocess
import tempfile
import threading
import time
import urllib.request
from pathlib import Path

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import NameOID
import zstandard


ACCOUNT = "saiai-local-proxy-account"
MOCK_KEY = "TEST_ONLY_NATIVE_OAUTH_PROOF"
APP_HEADERS = {"authorization", "cookie", "chatgpt-account-id", "host", "content-length", "connection",
               "upgrade", "sec-websocket-key", "sec-websocket-accept", "sec-websocket-extensions",
               "sec-websocket-version", "accept-encoding", "transfer-encoding"}


def file_sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as binary:
        for block in iter(lambda: binary.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def request_json(base: str, path: str):
    # Disable environment proxies; this endpoint is the loopback Go fixture.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(base + path, timeout=4) as response:
        data = response.read()
    return json.loads(data) if data else None


def read_exact(reader, size):
    data = reader.read(size)
    if len(data) != size:
        raise EOFError("mock connection ended")
    return data


def read_head(reader):
    first = reader.readline(16384)
    if not first:
        raise EOFError("mock HTTP connection ended")
    lines = [first]
    headers = {}
    for _ in range(200):
        line = reader.readline(65536)
        if not line:
            raise EOFError("mock HTTP headers incomplete")
        lines.append(line)
        if line == b"\r\n":
            return b"".join(lines), first.decode().strip(), headers
        name, value = line.decode().split(":", 1)
        headers.setdefault(name.lower(), []).append(value.strip())
    raise ValueError("mock header limit")


def read_body(reader, headers):
    if "chunked" in ",".join(headers.get("transfer-encoding", [])).lower():
        raw, decoded = bytearray(), bytearray()
        while True:
            line = reader.readline(128)
            raw += line
            size = int(line.split(b";", 1)[0], 16)
            if size == 0:
                while True:
                    line = reader.readline(65536)
                    raw += line
                    if line == b"\r\n":
                        return bytes(raw), bytes(decoded)
            block = read_exact(reader, size + 2)
            raw += block
            decoded += block[:-2]
    size = int(headers.get("content-length", ["0"])[0])
    if size > 16 * 1024 * 1024:
        raise ValueError("mock body limit")
    body = read_exact(reader, size)
    return body, body


def frame(reader):
    first = read_exact(reader, 2)
    length = first[1] & 127
    extra = b""
    if length == 126:
        extra = read_exact(reader, 2)
        length = struct.unpack("!H", extra)[0]
    elif length == 127:
        extra = read_exact(reader, 8)
        length = struct.unpack("!Q", extra)[0]
    if length > 16 * 1024 * 1024 or first[0] & 0x70:
        raise ValueError("mock frames must be uncompressed and bounded")
    mask = read_exact(reader, 4) if first[1] & 128 else b""
    body = read_exact(reader, length)
    payload = bytes(value ^ mask[index % 4] for index, value in enumerate(body)) if mask else body
    return first + extra + mask + body, first[0] & 15, bool(first[0] & 128), payload


def send_frame(stream, opcode, payload):
    length = len(payload)
    header = bytes([128 | opcode, length]) if length < 126 else bytes([128 | opcode, 126]) + struct.pack("!H", length)
    stream.sendall(header + payload)


def digest_record(kind, method, target, headers, body=b"", opcode=0):
    clean_headers = {name: values for name, values in headers.items() if name not in {"authorization", "cookie", "chatgpt-account-id"}}
    decoded = zstandard.ZstdDecompressor().decompress(body, max_output_size=16*1024*1024) if headers.get("content-encoding") == ["zstd"] and body else body
    value = json.loads(decoded) if decoded else {}
    metadata = value.get("client_metadata") or {}
    has_state = "x-codex-turn-state" in metadata or "x-codex-turn-state" in headers
    state_shape = {"header": headers.get("x-codex-turn-state"),
                   "metadata_present": "x-codex-turn-state" in metadata,
                   "metadata": metadata.get("x-codex-turn-state")}
    state_digest = hashlib.sha256(json.dumps(state_shape, sort_keys=True, separators=(",", ":")).encode()).hexdigest() if has_state else None
    return {"kind": kind, "method": method, "path": target.partition("?")[0], "query": target.partition("?")[2],
            "headers": clean_headers, "sha256": hashlib.sha256(body).hexdigest(),
            "decoded_sha256": hashlib.sha256(decoded).hexdigest(), "size": len(body), "message_type": opcode,
            "model": value.get("model"), "reasoning": value.get("reasoning"),
            "has_turn_state": has_state, "turn_state_sha256": state_digest,
            "previous_response_id": value.get("previous_response_id"),
            "has_tool_output": any(item.get("type") == "function_call_output" for item in value.get("input", []) if isinstance(item, dict)),
            "warmup": value.get("generate") is False}


class Capture(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, tls, ca_path, gateway, downstream=None, http_fallback=False, imagegen=False):
        self.tls, self.ca_path, self.gateway = tls, ca_path, gateway
        self.downstream, self.http_fallback = downstream, http_fallback
        self.imagegen = imagegen
        self.records, self.errors, self.blocked, self.number = [], [], 0, 0
        self.lock = threading.Lock()
        super().__init__(("127.0.0.1", 0), CaptureHandler)

    def record(self, *args):
        item = digest_record(*args)
        with self.lock:
            self.records.append(item)

    def events(self, payload):
        value = json.loads(payload)
        with self.lock:
            if value.get("generate") is False:
                response, tool = "resp_mock_warmup", False
            else:
                self.number += 1
                response, tool = f"resp_mock_{self.number}", self.number == 1
        events = request_json(self.gateway, f"/_proof/events?response={response}&tool={int(tool)}&imagegen={int(self.imagegen)}")
        return [bytes(event) if isinstance(event, list) else base64.b64decode(event) for event in events]


class CaptureHandler(socketserver.BaseRequestHandler):
    def handle(self):
        server = self.server
        try:
            self.request.settimeout(25)
            reader = self.request.makefile("rb")
            _, line, _ = read_head(reader)
            method, target, _ = line.split(" ", 2)
            if method != "CONNECT" or target not in {"chatgpt.com:443", "api.openai.com:443"}:
                with server.lock:
                    server.blocked += 1
                self.request.sendall(b"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
                return
            self.request.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            stream = server.tls.wrap_socket(self.request, server_side=True)
            with stream:
                reader = stream.makefile("rb")
                while True:
                    head, line, headers = read_head(reader)
                    method, target_path, _ = line.split(" ", 2)
                    raw_body, body = read_body(reader, headers)
                    websocket = headers.get("upgrade") == ["websocket"]
                    if websocket:
                        server.record("handshake", method, target_path, headers)
                        if server.http_fallback:
                            stream.sendall(b"HTTP/1.1 426 Upgrade Required\r\nContent-Length: 0\r\n\r\n")
                            return
                    elif target_path.partition("?")[0].endswith("/responses") or "/codex/images/" in target_path.partition("?")[0]:
                        server.record("http", method, target_path, headers, body)
                    elif target_path.partition("?")[0].endswith("/models"):
                        server.record("models", method, target_path, headers)
                    if server.downstream:
                        with socket.create_connection(server.downstream, timeout=4) as raw:
                            raw.sendall(f"CONNECT {target} HTTP/1.1\r\nHost: {target}\r\n\r\n".encode())
                            connect_reader = raw.makefile("rb")
                            _, status, _ = read_head(connect_reader)
                            if " 200 " not in status:
                                raise ValueError("SAIAI mock CONNECT failed")
                            context = ssl.create_default_context(cafile=str(server.ca_path))
                            with context.wrap_socket(raw, server_hostname=target.split(":")[0]) as upstream:
                                upstream.sendall(head + raw_body)
                                upstream_reader = upstream.makefile("rb")
                                response_head, status, response_headers = read_head(upstream_reader)
                                stream.sendall(response_head)
                                if websocket:
                                    if " 101 " not in status:
                                        raise ValueError("SAIAI mock WS upgrade failed")
                                    if response_headers.get("x-codex-proof-control") != ["first", "second"]:
                                        raise ValueError("SAIAI dropped WS handshake application headers")
                                    self.relay_ws(stream, reader, upstream, upstream_reader, method, target_path, headers)
                                    return
                                response_wire, _ = read_body(upstream_reader, response_headers)
                                stream.sendall(response_wire)
                    elif websocket:
                        accept = base64.b64encode(hashlib.sha1((headers["sec-websocket-key"][0] + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest())
                        stream.sendall(b"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + b"\r\nX-Codex-Turn-State: MOCK_HANDSHAKE_STATE\r\n\r\n")
                        while True:
                            _, opcode, final, payload = frame(reader)
                            if not final:
                                raise ValueError("unexpected fragmented official frame in baseline")
                            if opcode == 8:
                                send_frame(stream, 8, payload)
                                return
                            if opcode == 9:
                                send_frame(stream, 10, payload)
                            elif opcode in {1, 2}:
                                server.record("frame", method, target_path, headers, payload, opcode)
                                for event in server.events(payload):
                                    send_frame(stream, 1, event)
                    else:
                        if target_path.partition("?")[0].endswith("/responses"):
                            decoded = zstandard.ZstdDecompressor().decompress(body, max_output_size=16*1024*1024) if headers.get("content-encoding") == ["zstd"] else body
                            response = b"".join(b"data: " + event + b"\n\n" for event in server.events(decoded))
                            self.respond(stream, response, "text/event-stream", b"X-Codex-Turn-State: MOCK_FRAME_STATE\r\n")
                        elif target_path.partition("?")[0].endswith("/models"):
                            self.respond(stream, b'{"models":[]}', "application/json")
                        elif "/codex/images/" in target_path.partition("?")[0]:
                            response = request_json(server.gateway, "/_proof/image-response")
                            self.respond(stream, json.dumps(response).encode(), "application/json", b"X-Codex-Imagegen-Request-Id: TEST_ONLY-image-request\r\n")
                        else:
                            response = {}
                            if target_path.partition("?")[0].endswith("accounts/check"):
                                response = {"accounts": [{"id": ACCOUNT, "workspace_backend_origin": "NO_CONSTRAINT", "account_routing_override": "NO_CONSTRAINT"}]}
                            self.respond(stream, json.dumps(response).encode(), "application/json")
                    if "close" in ",".join(headers.get("connection", [])).lower():
                        return
        except (EOFError, ConnectionError, TimeoutError, ssl.SSLError):
            return
        except Exception as error:
            with server.lock:
                server.errors.append(type(error).__name__ + ": " + str(error))

    @staticmethod
    def respond(stream, body, content_type, extra=b""):
        stream.sendall(f"HTTP/1.1 200 OK\r\nContent-Type: {content_type}\r\nContent-Length: {len(body)}\r\n".encode() + extra + b"\r\n" + body)

    def relay_ws(self, client, reader, upstream, upstream_reader, method, path, headers):
        def upload():
            try:
                while True:
                    raw, opcode, final, payload = frame(reader)
                    if opcode in {1, 2}:
                        if not final:
                            raise ValueError("unexpected fragmented official frame in relay")
                        self.server.record("frame", method, path, headers, payload, opcode)
                    upstream.sendall(raw)
                    if opcode == 8:
                        return
            except (EOFError, ConnectionError, TimeoutError):
                return
        thread = threading.Thread(target=upload, daemon=True)
        thread.start()
        try:
            while True:
                raw, opcode, _, _ = frame(upstream_reader)
                client.sendall(raw)
                if opcode == 8:
                    return
        except (EOFError, ConnectionError, TimeoutError):
            return
        finally:
            try:
                upstream.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            thread.join(timeout=3)


def certificates(root):
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "SAIAI MOCK ONLY")])
    now = dt.datetime.now(dt.timezone.utc)
    common = x509.CertificateBuilder().subject_name(subject).issuer_name(subject).public_key(key.public_key()).not_valid_before(now-dt.timedelta(hours=1)).not_valid_after(now+dt.timedelta(days=1))
    ca = common.serial_number(x509.random_serial_number()).add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True).sign(key, hashes.SHA256())
    leaf = common.serial_number(x509.random_serial_number()).add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True).add_extension(x509.SubjectAlternativeName([x509.DNSName("chatgpt.com"), x509.DNSName("api.openai.com")]), critical=False).sign(key, hashes.SHA256())
    ca_path, key_path, leaf_path = root/"ca.pem", root/"ca-key.pem", root/"leaf.pem"
    ca_path.write_bytes(ca.public_bytes(serialization.Encoding.PEM))
    leaf_path.write_bytes(leaf.public_bytes(serialization.Encoding.PEM))
    key_path.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
    key_path.chmod(0o600)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(leaf_path, key_path)
    return tls, ca_path, key_path


def environment(root, ca_path, proxy):
    env = {name: os.environ[name] for name in ["PATH", "LANG"] if name in os.environ}
    for name, path in {"HOME": root/"home", "USERPROFILE": root/"home", "CODEX_HOME": root/"codex", "SAIAI_HOME": root/"saiai"}.items():
        path.mkdir(parents=True, exist_ok=True)
        env[name] = str(path)
    for name in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"]:
        env[name] = proxy
    env.update(SSL_CERT_FILE=str(ca_path), CODEX_CA_CERTIFICATE=str(ca_path), NO_PROXY="127.0.0.1,localhost,::1", no_proxy="127.0.0.1,localhost,::1")
    def encode(value):
        return base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip("=")
    token = encode({"alg": "none", "typ": "JWT"}) + "." + encode({"email": "mock@invalid", "exp": 4102444800, "https://api.openai.com/auth": {"chatgpt_account_id": ACCOUNT, "chatgpt_user_id": "mock_user", "chatgpt_plan_type": "plus"}}) + ".MOCK_ONLY"
    (root/"codex/auth.json").write_text(json.dumps({"auth_mode": "chatgptAuthTokens", "tokens": {"id_token": token, "access_token": token, "refresh_token": "", "account_id": ACCOUNT}, "last_refresh": dt.datetime.now(dt.timezone.utc).isoformat(), "OPENAI_API_KEY": None}))
    (root/"codex/auth.json").chmod(0o600)
    (root/"codex/config.toml").write_text('model="gpt-5.5"\n[features]\napps=false\n[otel]\nmetrics_exporter="none"\nexporter="none"\n[analytics]\nenabled=false\n')
    return env


def stop_process_tree(process):
    # Children (including the local code-mode host) must not outlive the lab.
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=5)


def run_official(binary, surface, env, cwd):
    if surface == "cli":
        process = subprocess.Popen([binary, "exec", "--skip-git-repo-check", "--ephemeral", "--json", "--sandbox", "read-only", "Only reply MOCK_ONLY_SUCCESS."], env=env, cwd=cwd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
        try:
            output, error = process.communicate(timeout=35)
            if process.returncode or '"type":"turn.completed"' not in output or "MOCK_ONLY_SUCCESS" not in output:
                raise RuntimeError("official CLI failed: " + output[-1500:] + error[-1500:])
            return {"turns_completed": 1, "account_read": "not_applicable"}
        finally:
            stop_process_tree(process)
    process = subprocess.Popen([binary, "app-server", "--stdio"], env=env, cwd=cwd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, start_new_session=True)
    messages = queue.Queue()
    threading.Thread(target=lambda: [messages.put(json.loads(line)) for line in process.stdout], daemon=True).start()
    try:
        def send(value):
            process.stdin.write(json.dumps(value) + "\n")
            process.stdin.flush()
        def until(predicate, timeout=25):
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                value = messages.get(timeout=max(0.1, deadline-time.monotonic()))
                if predicate(value):
                    return value
            raise RuntimeError("official app-server response deadline")
        def rpc(number, method, params):
            send({"id": number, "method": method, "params": params})
            value = until(lambda value: value.get("id") == number)
            if "error" in value:
                raise RuntimeError("official app-server RPC failed: " + method)
            return value["result"]
        rpc(1, "initialize", {"clientInfo": {"name": "codex_vscode", "version": "0.159.2"}, "capabilities": {"experimentalApi": True}})
        send({"method": "initialized"})
        account = rpc(2, "account/read", {})
        if account.get("account", {}).get("type") != "chatgpt":
            raise RuntimeError("official app-server synthetic account not loaded")
        thread = rpc(3, "thread/start", {"cwd": str(cwd), "approvalPolicy": "never", "sandbox": "read-only"})["thread"]["id"]
        for number in [4, 5]:
            rpc(number, "turn/start", {"threadId": thread, "input": [{"type": "text", "text": "Only reply MOCK_ONLY_SUCCESS."}]})
            completed = until(lambda value: value.get("method") == "turn/completed")
            if completed["params"]["turn"].get("status") != "completed":
                raise RuntimeError("official app-server model turn failed")
        return {"turns_completed": 2, "account_read": "chatgpt"}
    finally:
        stop_process_tree(process)


def comparisons(captured, receipts):
    checks, unexpected = {}, []
    stages = {"official": captured, "gateway": [r for r in receipts if r["stage"] == "gateway"], "provider": [r for r in receipts if r["stage"] == "provider"]}
    for kind in ["models", "handshake", "http", "frame"]:
        groups = [[r for r in values if r["kind"] == kind] for values in stages.values()]
        counts = list(map(len, groups))
        if len(set(counts)) != 1:
            unexpected.append(f"{kind} counts {counts}")
            continue
        if kind == "models":
            # Official startup may fetch the catalog concurrently with two
            # distinct User-Agents. Arrival order at separate network hops is
            # not request identity; compare the complete request multiset.
            def catalog_identity(record):
                headers = {name.lower(): values for name, values in record["headers"].items()
                           if name.lower() not in APP_HEADERS}
                return json.dumps([record["method"], record["query"], record["sha256"], headers], sort_keys=True)
            groups = [sorted(group, key=catalog_identity) for group in groups]
        for index, records in enumerate(zip(*groups)):
            suffix = records[0]["path"].removeprefix("/backend-api/codex/")
            gateway_path = f"/v1/codex/{suffix}" if suffix.startswith("images/") else f"/v1/{suffix}"
            if records[1]["path"] != gateway_path or records[2]["path"] != f"/backend-api/codex/{suffix}" or records[0]["path"] != records[2]["path"]:
                unexpected.append(f"{kind}[{index}] endpoint mapping")
            dimensions = ["sha256", "query", "message_type", "method"]
            if kind in {"http", "frame"}:
                dimensions.append("decoded_sha256")
            for dimension in dimensions:
                if len({r[dimension] for r in records}) != 1:
                    unexpected.append(f"{kind}[{index}] {dimension}")
            headers = [{name.lower(): values for name, values in r["headers"].items()} for r in records]
            for name in set().union(*(h.keys() for h in headers)) - APP_HEADERS:
                if any(h.get(name) != headers[0].get(name) for h in headers[1:]):
                    detail = f" {[h.get(name) for h in headers]}" if name == "user-agent" else ""
                    unexpected.append(f"{kind}[{index}] header {name}{detail}")
        checks[kind] = counts[0]
    if unexpected:
        raise RuntimeError("unexpected request changes: " + ", ".join(unexpected))
    return checks


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex", required=True)
    parser.add_argument("--client", required=True)
    parser.add_argument("--gateway", required=True)
    parser.add_argument("--production-routes", action="store_true",
                        help="verify public-route auth, standard billing and usage evidence")
    parser.add_argument("--imagegen", action="store_true", help="exercise the actual built-in image_gen extension against loopback Images")
    args = parser.parse_args()
    if not args.gateway.startswith("http://127.0.0.1:"):
        raise ValueError("only a loopback mock Gateway is accepted")
    report = {"result": "pass", "client_config_file_version": 2, "real_provider_requests": 0, "cases": [], "body_capture_persisted": False}
    report["gateway_fixture"] = "production_routes_loopback_tls" if args.production_routes else "service_builders_recording_mocks"
    report["codex_sha256"] = file_sha256(args.codex)
    report["saiai_sha256"] = file_sha256(args.client)
    report["driver_sha256"] = file_sha256(__file__)
    report["application_header_comparison"] = "exact_values_presence_and_multiplicity"
    report["built_in_imagegen_exercised"] = args.imagegen
    with tempfile.TemporaryDirectory(prefix="saiai-native-official-", dir=os.environ.get("SAIAI_PROOF_TMPDIR")) as temporary:
        root = Path(temporary)
        tls, ca, key = certificates(root)
        version_env = environment(root/"version", ca, "http://127.0.0.1:1")
        report["codex_version"] = subprocess.check_output([args.codex, "--version"], env=version_env, stderr=subprocess.DEVNULL, text=True).strip()
        report["saiai_version"] = subprocess.check_output([args.client, "--version"], env=version_env, stderr=subprocess.DEVNULL, text=True).strip()
        cases = [("app-server", True)] if args.imagegen else [("cli", False), ("cli", True), ("app-server", False), ("app-server", True)]
        for surface, fallback in cases:
            baseline = None
            for chain in [False, True]:
                request_json(args.gateway, f"/_proof/reset?imagegen={int(args.imagegen)}")
                state = root/f"{surface}-{int(fallback)}-{int(chain)}"
                state.mkdir()
                with socket.socket() as reserve:
                    reserve.bind(("127.0.0.1", 0))
                    proxy_port = reserve.getsockname()[1]
                capture = Capture(tls, ca, args.gateway, ("127.0.0.1", proxy_port) if chain else None, fallback, args.imagegen)
                thread = threading.Thread(target=capture.serve_forever, daemon=True)
                thread.start()
                env = environment(state, ca, f"http://127.0.0.1:{capture.server_address[1]}")
                (state/"saiai/config.json").write_text(json.dumps({"version": 2, "base_url": args.gateway, "api_key": MOCK_KEY, "listen": f"127.0.0.1:{proxy_port}", "ca_cert_path": str(ca), "ca_key_path": str(key), "chatgpt_chat_passthrough": False, "providers": {"codex": {"base_url": args.gateway, "api_key": MOCK_KEY}}}))
                proxy = None
                try:
                    if chain:
                        proxy_env = env.copy()
                        for name in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"]:
                            proxy_env.pop(name, None)
                        proxy = subprocess.Popen([args.client, "--verbose"], env=proxy_env, cwd=state, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True, start_new_session=True)
                        deadline = time.monotonic()+5
                        while True:
                            try:
                                socket.create_connection(("127.0.0.1", proxy_port), timeout=0.2).close()
                                break
                            except OSError:
                                if time.monotonic() > deadline:
                                    detail = proxy.stderr.read() if proxy.poll() is not None else "listener did not start"
                                    raise RuntimeError("candidate local proxy failed to start: " + detail)
                                time.sleep(0.05)
                    control = run_official(args.codex, surface, env, root)
                    control["client_http_fallback_observed"] = any(r["kind"] == "handshake" for r in capture.records) and any(r["kind"] == "http" for r in capture.records)
                    if capture.errors:
                        raise RuntimeError("capture failed: " + ",".join(capture.errors))
                    requests = [r for r in capture.records if r["kind"] in {"http", "frame"} and not r["warmup"]]
                    if not requests or not any(r["has_tool_output"] for r in requests) or not any(r["has_turn_state"] for r in requests):
                        raise RuntimeError("official tool continuation or native turn-state was not exercised")
                    if args.imagegen:
                        images = [r for r in requests if "/codex/images/" in r["path"]]
                        saved_images = list((state/"codex/generated_images").rglob("*.png"))
                        if len(images) != 1 or not saved_images:
                            raise RuntimeError("actual built-in image_gen did not call native Images and save its result")
                        control["native_image_requests"] = len(images)
                        control["image_saved"] = True
                        control["image_sha256"] = file_sha256(saved_images[0])
                    if chain:
                        receipts = request_json(args.gateway, "/_proof/report")
                        # 426 is returned by the capture fixture before SAIAI;
                        # only accepted model attempts are compared below.
                        compared = [r for r in capture.records if not (fallback and r["kind"] == "handshake")]
                        checks = comparisons(compared, receipts)
                        if not all(r.get("selected_auth") and r.get("selected_account") and r.get("no_cookie") for r in receipts if r["stage"] == "provider"):
                            raise RuntimeError("provider credential boundary differs from selected mock account")
                        stable = lambda rows: [{k: r[k] for k in ["model", "reasoning", "has_turn_state", "turn_state_sha256", "previous_response_id", "has_tool_output", "warmup", "message_type"]} for r in rows if r["kind"] in {"http", "frame"}]
                        if stable(capture.records) != baseline:
                            raise RuntimeError("official direct and SAIAI request interaction differs")
                        if args.production_routes:
                            deadline = time.monotonic() + 4
                            while True:
                                gateway_control = request_json(args.gateway, "/_proof/control")
                                if gateway_control["usage_records"] >= len(requests) or time.monotonic() > deadline:
                                    break
                                time.sleep(0.02)
                            if gateway_control["usage_records"] != len(requests) or gateway_control["billing_applications"] != len(requests):
                                raise RuntimeError("public-route usage or billing count differs from accepted model payloads")
                            if gateway_control["auth_checks"] < 1 or gateway_control["balance_checks"] < 1 or gateway_control["run_mode"] != "standard" or gateway_control["blocked_provider_destinations"]:
                                raise RuntimeError("public-route authentication, billing or closed egress evidence incomplete")
                            control["gateway"] = gateway_control
                        report["cases"].append({"surface": surface, "transport": "http-client-fallback" if fallback else "ws", "control": control,
                                                "compared_requests": checks, "unexpected_changes": 0, "blocked_other_destinations": capture.blocked,
                                                "turn_state_values_match_direct": True,
                                                "official_payload_digests": [r["sha256"] for r in requests], "model_request_count": len(requests)})
                    else:
                        baseline = [{k: r[k] for k in ["model", "reasoning", "has_turn_state", "turn_state_sha256", "previous_response_id", "has_tool_output", "warmup", "message_type"]} for r in capture.records if r["kind"] in {"http", "frame"}]
                finally:
                    if proxy:
                        stop_process_tree(proxy)
                    capture.shutdown()
                    capture.server_close()
                    thread.join(timeout=3)
    report["temporary_credentials_and_ca_removed"] = not root.exists()
    print(json.dumps(report, separators=(",", ":")))


if __name__ == "__main__":
    main()
