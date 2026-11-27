# Independent verifier: checks the DSSE Ed25519 signatures of a PrismIA migration attestation
# against the published key, and that the signed subject is the downloaded deliverable ZIP.
# Usage: python3 -I verify_attestation.py <dir> <attestation-id>
import base64, hashlib, json, sys
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

d, aid = sys.argv[1], sys.argv[2]
key = json.load(open(f"{d}/attestation_keys.json"))["attestationKeys"][0]
raw = bytes.fromhex(key["publicKeyHex"])
assert hashlib.sha256(raw).hexdigest() == key["fingerprintSha256"], "key fingerprint mismatch"
pk = Ed25519PublicKey.from_public_bytes(raw)

def pae(t, p):
    t = t.encode()
    return b"DSSEv1 %d %s %d %s" % (len(t), t, len(p), p)

def verify(env):
    p = base64.b64decode(env["payload"])
    for s in env["signatures"]:
        pk.verify(base64.b64decode(s["sig"]), pae(env["payloadType"], p))  # raises on a bad signature
    return json.loads(p)

st = verify(json.load(open(f"{d}/attestation-{aid}-envelope.raw")))
zipsha = hashlib.sha256(open(f"{d}/deliverable.zip.raw", "rb").read()).hexdigest()
subj = {s["name"]: s["digest"]["sha256"] for s in st["subject"]}
assert subj.get("deliverable.zip") == zipsha, "signed subject is not the downloaded ZIP"
print("envelope: signature valid, subject deliverable.zip =", zipsha)
n = 0
for line in open(f"{d}/attestation-{aid}-bundle.jsonl", "rb").read().splitlines():
    if line.strip():
        e = json.loads(line); verify(e.get("dsseEnvelope", e)); n += 1
print("bundle: statements with valid signatures:", n)
for c in st["predicate"]["claims"]:
    print(c["id"], c["status"], c.get("reason", "")[:100])
c8 = next(c for c in st["predicate"]["claims"] if c["id"] == "C8")
print("C8 evidence:", json.dumps({k: c8["evidence"].get(k) for k in ("services", "cycles", "interServiceEdges", "sharedTables", "sharedTablesSchema")}))
