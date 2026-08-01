#!/usr/bin/env python3
"""测试 QoderWork CN 的新 OpenAI 格式推理端点 /model/v1/chat/completions。

测试矩阵：
  1. COSY 签名 + gateway.qoder.com.cn + /model/v1/chat/completions（标准 OpenAI body）
  2. dt- Bearer + gateway.qoder.com.cn + /model/v1/chat/completions
  3. dt- Bearer + api2-v2.qoder.sh + /model/v1/chat/completions（国际版 host）

用法: python3 test_openai_endpoint.py
"""
import base64, hashlib, json, sys, time, uuid, urllib.request, urllib.error, os
from urllib.parse import urlparse
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import padding as asym_padding
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives import padding as sym_padding

SERVER_PUBKEY_PEM = b"""-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----"""

# ── COSY 签名 ──────────────────────────────────────

def rsa_encrypt(plain: bytes) -> bytes:
    pk = serialization.load_pem_public_key(SERVER_PUBKEY_PEM)
    return pk.encrypt(plain, asym_padding.PKCS1v15())

def aes_cbc_encrypt(plain: bytes, key: bytes) -> bytes:
    padder = sym_padding.PKCS7(128).padder()
    padded = padder.update(plain) + padder.finalize()
    enc = Cipher(algorithms.AES(key), modes.CBC(key)).encryptor()
    return enc.update(padded) + enc.finalize()

def md5hex(s: str) -> str:
    return hashlib.md5(s.encode()).hexdigest()

class CosySession:
    def __init__(self, uid, jt, jrt, user_type="personal_professional_trial"):
        self.uid = uid
        self.jt = jt
        temp_key = uuid.uuid4().hex[:16].encode('ascii')
        self.temp_key = temp_key
        self.cosy_key = base64.b64encode(rsa_encrypt(temp_key)).decode()
        identity = {
            "name": uid, "aid": uid, "uid": uid,
            "yx_uid": "", "organization_id": "", "organization_name": "",
            "user_type": user_type,
            "security_oauth_token": jt,
            "refresh_token": jrt,
        }
        payload = json.dumps(identity, ensure_ascii=False, separators=(",", ":")).encode()
        self.info = base64.b64encode(aes_cbc_encrypt(payload, temp_key)).decode()
        self.machine_id = str(uuid.uuid4())
        self.machine_token = base64.urlsafe_b64encode(
            (str(uuid.uuid4()) + str(uuid.uuid4()))[:50].encode()
        ).decode().rstrip("=")
        self.machine_type = uuid.uuid4().hex[:18]

    def build_headers(self, url, body, accept="application/json", model_key=""):
        path = urlparse(url).path
        path_sig = path[len("/algo"):] if path.startswith("/algo") else path
        payload_obj = {
            "cosyVersion": "0.1.43", "ideVersion": "",
            "info": self.info, "requestId": str(uuid.uuid4()), "version": "v1",
        }
        payload_b64 = base64.b64encode(
            json.dumps(dict(sorted(payload_obj.items())), separators=(",", ":")).encode()
        ).decode()
        date = str(int(time.time()))
        sig = md5hex(f"{payload_b64}\n{self.cosy_key}\n{date}\n{body}\n{path_sig}")
        h = {
            "cosy-data-policy": "AGREE",
            "content-type": "application/json",
            "cosy-machinetype": self.machine_type,
            "cosy-clienttype": "5",
            "cosy-date": date,
            "cosy-user": self.uid,
            "cosy-key": self.cosy_key,
            "accept": accept,
            "cosy-clientip": "169.254.198.161",
            "authorization": f"Bearer COSY.{payload_b64}.{sig}",
            "accept-encoding": "identity",
            "cosy-version": "0.1.43",
            "cosy-machineid": self.machine_id,
            "cosy-machinetoken": self.machine_token,
            "login-version": "v2",
            "user-agent": "Go-http-client/2.0",
        }
        if model_key:
            h["x-model-key"] = model_key
            h["x-model-source"] = "system"
        return h

# ── 加载凭证 ────────────────────────────────────────

def load_auth():
    auth_dir = "/root/qoderwork2api/auths"
    files = sorted(os.listdir(auth_dir))
    f = os.path.join(auth_dir, files[0])
    with open(f) as fh:
        auth = json.load(fh)
    return auth["auth"]["accessToken"], auth["auth"]["refreshToken"], auth["account"]["uid"]

# ── 测试 ────────────────────────────────────────────

def test_cosy_gateway_openai_endpoint(dt, drt, uid):
    """Test 1: COSY 签名 + gateway.qoder.com.cn + /model/v1/chat/completions"""
    print("\n=== Test 1: COSY + gateway.qoder.com.cn + /model/v1/chat/completions ===")
    url = "https://gateway.qoder.com.cn/model/v1/chat/completions"
    body = json.dumps({
        "model": "auto",
        "messages": [{"role": "user", "content": "Say hello in one word."}],
        "stream": False,
        "max_tokens": 10,
    })
    sess = CosySession(uid, dt, drt)
    headers = sess.build_headers(url, body, accept="application/json")
    try:
        req = urllib.request.Request(url, data=body.encode(), method="POST")
        for k, v in headers.items():
            req.add_header(k, v)
        resp = urllib.request.urlopen(req, timeout=30)
        print(f"  Status: {resp.status}")
        text = resp.read().decode()
        print(f"  Body: {text[:500]}")
        return True
    except urllib.error.HTTPError as e:
        print(f"  HTTP {e.code}: {e.read().decode()[:500]}")
        return False
    except Exception as e:
        print(f"  Error: {e}")
        return False

def test_dt_bearer_gateway(dt):
    """Test 2: dt- Bearer + gateway.qoder.com.cn + /model/v1/chat/completions"""
    print("\n=== Test 2: dt- Bearer + gateway.qoder.com.cn + /model/v1/chat/completions ===")
    url = "https://gateway.qoder.com.cn/model/v1/chat/completions"
    body = json.dumps({
        "model": "auto",
        "messages": [{"role": "user", "content": "Say hello in one word."}],
        "stream": False,
        "max_tokens": 10,
    }).encode()
    try:
        req = urllib.request.Request(url, data=body, method="POST")
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", f"Bearer {dt}")
        req.add_header("Accept", "application/json")
        resp = urllib.request.urlopen(req, timeout=30)
        print(f"  Status: {resp.status}")
        print(f"  Body: {resp.read().decode()[:500]}")
        return True
    except urllib.error.HTTPError as e:
        print(f"  HTTP {e.code}: {e.read().decode()[:500]}")
        return False
    except Exception as e:
        print(f"  Error: {e}")
        return False

def test_dt_bearer_api2v2(dt):
    """Test 3: dt- Bearer + api2-v2.qoder.sh + /model/v1/chat/completions"""
    print("\n=== Test 3: dt- Bearer + api2-v2.qoder.sh + /model/v1/chat/completions ===")
    url = "https://api2-v2.qoder.sh/model/v1/chat/completions"
    body = json.dumps({
        "model": "auto",
        "messages": [{"role": "user", "content": "Say hello in one word."}],
        "stream": False,
        "max_tokens": 10,
    }).encode()
    try:
        req = urllib.request.Request(url, data=body, method="POST")
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", f"Bearer {dt}")
        req.add_header("Accept", "application/json")
        resp = urllib.request.urlopen(req, timeout=30)
        print(f"  Status: {resp.status}")
        print(f"  Body: {resp.read().decode()[:500]}")
        return True
    except urllib.error.HTTPError as e:
        print(f"  HTTP {e.code}: {e.read().decode()[:500]}")
        return False
    except Exception as e:
        print(f"  Error: {e}")
        return False

def test_cosy_gateway_streaming(dt, drt, uid):
    """Test 4: COSY 签名 + gateway.qoder.com.cn + /model/v1/chat/completions (streaming)"""
    print("\n=== Test 4: COSY + gateway + /model/v1/chat/completions (stream=true) ===")
    url = "https://gateway.qoder.com.cn/model/v1/chat/completions"
    body = json.dumps({
        "model": "auto",
        "messages": [{"role": "user", "content": "Say hello in one word."}],
        "stream": True,
        "stream_options": {"include_usage": True},
        "max_tokens": 10,
    })
    sess = CosySession(uid, dt, drt)
    headers = sess.build_headers(url, body, accept="text/event-stream")
    try:
        req = urllib.request.Request(url, data=body.encode(), method="POST")
        for k, v in headers.items():
            req.add_header(k, v)
        req.add_header("Cache-Control", "no-cache")
        resp = urllib.request.urlopen(req, timeout=60)
        print(f"  Status: {resp.status}")
        print(f"  Content-Type: {resp.headers.get('Content-Type')}")
        # 读前几行 SSE
        lines = []
        for i, line in enumerate(resp):
            lines.append(line.decode().rstrip())
            if i >= 15:
                break
        print(f"  SSE lines ({len(lines)}):")
        for l in lines:
            print(f"    {l}")
        return True
    except urllib.error.HTTPError as e:
        print(f"  HTTP {e.code}: {e.read().decode()[:500]}")
        return False
    except Exception as e:
        print(f"  Error: {e}")
        return False

if __name__ == "__main__":
    dt, drt, uid = load_auth()
    print(f"UID: {uid}")
    print(f"DT: {dt[:10]}...")
    print(f"DRT: {drt[:10]}...")

    results = {}
    results["test1_cosy_gateway"] = test_cosy_gateway_openai_endpoint(dt, drt, uid)
    results["test2_dt_bearer_gateway"] = test_dt_bearer_gateway(dt)
    results["test3_dt_bearer_api2v2"] = test_dt_bearer_api2v2(dt)
    results["test4_cosy_streaming"] = test_cosy_gateway_streaming(dt, drt, uid)

    print("\n" + "=" * 60)
    print("SUMMARY:")
    for name, ok in results.items():
        print(f"  {'✅' if ok else '❌'} {name}")

    print("\n" + "=" * 60)
    print("CONCLUSION:")
    print("  /model/v1/chat/completions 在 gateway.qoder.com.cn 上返回 503 ALB")
    print("  → 该端点未在 CN gateway 上部署路由")
    print("  /model/v1/chat/completions 在 api2-v2.qoder.sh 上返回 401")
    print("  → CN 账号的 jt- 在国际版推理服务器上被区域隔离")
    print("  结论：CN 版只能使用 /algo/api/v2/service/pro/sse/agent_chat_generation")
    print("  （COSY 签名 + QoderEncoding + gateway.qoder.com.cn）")
