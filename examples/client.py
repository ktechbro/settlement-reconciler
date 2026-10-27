"""Exercise the local fixture-mode API. No wallet or blockchain transaction is used."""
import datetime as dt
import json
import os
import time
import urllib.request
import uuid

BASE = os.environ.get("RECONCILER_URL", "http://127.0.0.1:8081").rstrip("/")
TOKEN = os.environ.get("RECONCILER_TOKEN", "local-demo-token-change-me-0001")
TENANT = os.environ.get("RECONCILER_TENANT", "demo")
run = uuid.uuid4().hex[:12]


def stamp(value):
    return value.isoformat().replace("+00:00", "Z")


def call(method, path, body=None):
    headers = {"Authorization": f"Bearer {TOKEN}"}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers.update({"Content-Type": "application/json", "Idempotency-Key": f"{run}-{path}"})
    request = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)


asset = {"network": "base-demo", "address": "0x1111111111111111111111111111111111111111"}
payer = "0x2222222222222222222222222222222222222222"
payee = "0x3333333333333333333333333333333333333333"
mapping_id, decision_id = f"mapping-{run}", f"decision-{run}"
mapping = {"id": mapping_id, "recipient_reference": "vendor:example", "network": asset["network"], "address": payee, "version": 1}
call("POST", "/v1/mappings", mapping)
now = dt.datetime.now(dt.timezone.utc)
claims = {
    "id": decision_id, "issuer": "urn:local-example:unsigned", "tenant_id": TENANT,
    "agent_id": "procurement-agent", "mandate_id": "demo-mandate", "verdict": "allow",
    "asset": asset, "amount_atomic": "50000000", "payer": payer,
    "recipient_reference": "vendor:example", "mapping_id": mapping_id,
    "issued_at": stamp(now - dt.timedelta(seconds=10)),
    "expires_at": stamp(now + dt.timedelta(minutes=5)),
    "payment_deadline": stamp(now + dt.timedelta(minutes=10)),
}
call("POST", "/v1/decisions", {"fixture": claims})
broadcast = dt.datetime.now(dt.timezone.utc)
tx = "0x" + uuid.uuid4().hex + uuid.uuid4().hex
call("POST", "/v1/payment-attempts", {
    "id": f"attempt-{run}", "decision_id": decision_id, "asset": asset,
    "transaction_hash": tx, "event_index": "0", "expected_sender": payer,
    "expected_recipient": payee, "amount_atomic": "50000000", "broadcast_at": stamp(broadcast),
})
block = broadcast + dt.timedelta(seconds=1)
call("POST", "/v1/settlements", {
    "id": f"payment-{run}", "asset": asset, "transaction_hash": tx, "event_index": "0",
    "sender": payer, "recipient": payee, "amount_atomic": "50000000",
    "block_time": stamp(block), "finalized_at": stamp(block), "status": "finalized",
    "evidence": {"source": "synthetic-example", "receipt_status": 1},
})
time.sleep(1.2)
call("POST", "/v1/reconcile", {})
trace = call("GET", f"/v1/decisions/{decision_id}/evidence")
status = trace["case_history"][-1]["case"]["status"]
assert status == "matched", trace
print(json.dumps({"status": "PASS", "case_status": status, "decision_id": decision_id, "evidence": trace}, indent=2))
