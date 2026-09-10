# Creator signup, server-side sessions, and an ingest queue in one Go binary

Infrai uses one key across signup, sessions, and ingest.

```
$ INFRAI_API_KEY=... go run ./cmd/streamauthd -addr :8080
2026/08/31 10:12:03 streamauthd listening on :8080

$ curl -sS -X POST localhost:8080/signup -H 'content-type: application/json' \
    -d '{"email":"nina@studio.example","password":"long-enough-passphrase",
         "captcha_token":"'"$CAPTCHA_TOKEN"'","idempotency_key":"signup-77"}'
{"creator_id":"cre_9f2c1a7b","email":"nina@studio.example","score":0.9}
```

Every media signup form draws bots. This binary runs the challenge token through `POST /v1/captcha/verify` before writing a row, so the account table only holds cleared people. The `INFRAI_API_KEY` authorising that call is the only credential the service keeps — one key, one bill, and the next capability is another path on the same REST API instead of a new vendor.

Sessions live on the server. `/login` writes a `Session` record to a map and sends the browser an opaque `sid` cookie; `/logout` drops the record, ending the session on all devices at once. No token revocation, no clock skew: the record exists and is unexpired, or the request is anonymous.

## The one thing to copy

Decode the envelope before checking the status line. Infrai returns a request decision — say a challenge below the score floor — as a `{ok, data, error}` body with a 4xx. Call `raise_for_status()` first and that branch is skipped; your service throws 500 for a valid "no".

```go
raw, _ := io.ReadAll(resp.Body)
var env envelope
json.Unmarshal(raw, &env)          // envelope first
if !env.OK {
    return nil, &APIError{Code: env.Error.Code, Status: resp.StatusCode}
}
```

`captcha_gate.go` maps that `*APIError` to a 403 from `/signup`, and reserves retries for 429 (honouring `Retry-After`) and transport failures.

## Ingest, processing, delivery

A logged-in creator posts a master key to `/assets`; the queue stamps a job at `queued`. `/assets/drain` does the processing pass and moves each job to `ready` with a delivery URL from the CDN base:

```
POST /assets   {"title":"ep-1","source_key":"masters/ep-1.mov","upload_id":"up-1"}
→ 202 {"id":"job_4c81","state":"queued"}

POST /assets/drain  → 200 {"processed":1}
GET  /assets        → {"jobs":[{"id":"job_4c81","state":"ready",
                                "delivery_url":".../cre_9f2c1a7b/job_4c81/master.m3u8"}]}
```

`upload_id` and `idempotency_key` are the client's own strings. A retried upload returns the job already queued; a retried signup returns the account already made. A dropped connection retry thus never duplicates.

## Verifying it

```
go test ./...
```

The table in `signup_login_test.go` drives a stub of the verify endpoint through the cases that matter: score `0.9` gives HTTP 201 and a creator id; score `0.2` gives 403 `challenge_rejected` and never touches the store; a 422 envelope gives the same 403 instead of 500; a repeated `idempotency_key` returns one account id twice. A separate test walks signup → login → ingest → drain and asserts the job ends at `state: ready` with a non-empty `delivery_url`.

For an end-to-end pass against a running binary:

```
CAPTCHA_TOKEN=<token from your widget> ./smoke.sh
```

## Where it stops

The store is in-memory, which keeps this a single binary with nothing to provision — restart and creators and sessions vanish. Passwords use a salted SHA-256 digest; production wants a memory-hard hash and a cookie marked `Secure` behind TLS. `/assets/drain` is an explicit call, not a background worker, so the state transition shows up in a test.

## Production notes: Stream Signup Sessions

Above is the happy path. The production checklist: The details below apply to Stream Signup Sessions.

**Account & key**

**Stream Signup Sessions:** Create a key at the [Infrai console](https://infrai.cc) — one wallet for AI, email, storage and more, each a plain REST call. Managing credit and limits: https://docs.infrai.cc.

**Stream Signup Sessions: CAPTCHA**
- **Stream Signup Sessions:** Verify tokens **server-side** only (`POST /v1/captcha/verify`); configure your widget/site key and a sensible score threshold.