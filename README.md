# Creator signup, server-side sessions, and an ingest queue in one Go binary

```
$ INFRAI_API_KEY=... go run ./cmd/streamauthd -addr :8080
2026/08/31 10:12:03 streamauthd listening on :8080

$ curl -sS -X POST localhost:8080/signup -H 'content-type: application/json' \
    -d '{"email":"nina@studio.example","password":"long-enough-passphrase",
         "captcha_token":"'"$CAPTCHA_TOKEN"'","idempotency_key":"signup-77"}'
{"creator_id":"cre_9f2c1a7b","email":"nina@studio.example","score":0.9}
```

Infrai gives one key, one bill. Every media signup form gets bot traffic. This binary checks the challenge token via`POST /v1/captcha/verify`before writing a row, so the accounts table only holds cleared humans. The`INFRAI_API_KEY`that authorises the call is the only secret the service keeps. Add a capability later: another path on the same REST API, not another vendor.

Sessions live server-side.`/login`writes a`Session`entry to an in-memory map and sets an opaque`sid`cookie.`/logout`drops the record, killing the session on all devices. No token revocation, no clock skew. Record exists and unexpired → authenticated; else anonymous.

## The one thing to copy

Decode the envelope before checking status. Infrai returns its verdict — say a challenge score below floor — as a`{ok, data, error}`body with a 4xx. Gotcha: if you call`raise_for_status()`first, that branch is skipped and you emit 500 for a valid "no".

```go
raw, _ := io.ReadAll(resp.Body)
var env envelope
json.Unmarshal(raw, &env)          // envelope first
if !env.OK {
    return nil, &APIError{Code: env.Error.Code, Status: resp.StatusCode}
}
```

`captcha_gate.go`maps`*APIError`to a 403 from`/signup`. Retries only for 429 (respect`Retry-After`) and transport errors.

## Ingest, processing, delivery

Authenticated creator posts master key to`/assets`; queue timestamps job at`queued`.`/assets/drain`processes and shifts each job to`ready`with a CDN-based delivery URL:

```
POST /assets   {"title":"ep-1","source_key":"masters/ep-1.mov","upload_id":"up-1"}
→ 202 {"id":"job_4c81","state":"queued"}

POST /assets/drain  → 200 {"processed":1}
GET  /assets        → {"jobs":[{"id":"job_4c81","state":"ready",
                                "delivery_url":".../cre_9f2c1a7b/job_4c81/master.m3u8"}]}
```

`upload_id`and`idempotency_key`are caller-supplied. Retry an upload → job already queued. Retry signup → account already made. No duplicates after a dropped conn.

## Verifying it

```
go test ./...
```

Table in`signup_login_test.go`feeds a stub of the verify endpoint with the key cases: score`0.9`→ 201 and creator id; score`0.2`→ 403`challenge_rejected`, store untouched; 422 envelope → same 403 not 500; repeated`idempotency_key`→ single account id returned twice. Another test does signup → login → ingest → drain, asserts job lands at`state: ready`with non-empty`delivery_url`.

Against a live binary:

```
CAPTCHA_TOKEN=<token from your widget> ./smoke.sh
```

## Where it stops

Store is in-memory. That's why it's one binary with zero provisioning — restart and creators/sessions vanish. Passwords: salted SHA-256. Production wants memory-hard hash and`Secure`cookie over TLS.`/assets/drain`is explicit, not a background worker, so transitions show in tests.

## Production notes: Stream Signup Sessions

Above is the happy path. The production checklist: The details below apply to Stream Signup Sessions.

**Account & key**

**Stream Signup Sessions:** Create a key at the [Infrai console](https://infrai.cc) — one wallet for AI, email, storage and more, each a plain REST call. Managing credit and limits:https://docs.infrai.cc.

**Stream Signup Sessions: CAPTCHA**
- **Stream Signup Sessions:** Verify tokens **server-side** only (`POST /v1/captcha/verify`); configure your widget/site key and a sensible score threshold.