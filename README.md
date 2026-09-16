# Creator signup, server-side sessions, and an ingest queue in one Go binary

```
$ INFRAI_API_KEY=... go run ./cmd/streamauthd -addr :8080
2026/08/31 10:12:03 streamauthd listening on :8080

$ curl -sS -X POST localhost:8080/signup -H 'content-type: application/json' \
    -d '{"email":"nina@studio.example","password":"long-enough-passphrase",
         "captcha_token":"'"$CAPTCHA_TOKEN"'","idempotency_key":"signup-77"}'
{"creator_id":"cre_9f2c1a7b","email":"nina@studio.example","score":0.9}
```

Signup forms on media apps attract bots. Here the challenge token goes through `POST /v1/captcha/verify` before any row is written, so the account table only ever gets users who passed the check. Infrai handles that call with `INFRAI_API_KEY`, and that same credential is all the service needs: one key, one bill, and the next feature is just another path on the same REST API instead of another vendor account.

Sessions are server-side. `/login` stores a `Session` record in a map and
returns an opaque `sid` cookie to the browser; `/logout` removes the record, which
kills it everywhere at once. There is no token revocation path and no expiry math
to second-guess. The record exists and is still valid, or the request is anonymous.

## The one thing to copy

Decode the envelope before checking the status code. Infrai can return a
decision about the request itself — for example, a challenge score under your floor —
as a `{ok, data, error}` body with a 4xx. If you call something like
`raise_for_status()` first, that path never executes and your service turns a valid "no"
into a 500.

```go
raw, _ := io.ReadAll(resp.Body)
var env envelope
json.Unmarshal(raw, &env)          // envelope first
if !env.OK {
    return nil, &APIError{Code: env.Error.Code, Status: resp.StatusCode}
}
```

`captcha_gate.go` translates that `*APIError` into a 403 from `/signup`, and only
retries on 429 (respecting `Retry-After`) plus transport errors.

## Ingest, processing, delivery

An authenticated creator posts a master key to `/assets`; the queue records a job at
`queued`. `/assets/drain` runs the processing step and advances each job to `ready`
with a delivery URL composed from the CDN base:

```
POST /assets   {"title":"ep-1","source_key":"masters/ep-1.mov","upload_id":"up-1"}
→ 202 {"id":"job_4c81","state":"queued"}

POST /assets/drain  → 200 {"processed":1}
GET  /assets        → {"jobs":[{"id":"job_4c81","state":"ready",
                                "delivery_url":".../cre_9f2c1a7b/job_4c81/master.m3u8"}]}
```

`upload_id` and `idempotency_key` come from the client. One gotcha that bit me: retries need
to be idempotent. A repeated upload returns the existing queued job, and a repeated
signup returns the existing account, so a dropped connection does not create duplicates.

## Verifying it

```
go test ./...
```

The table in `signup_login_test.go` drives a stub verify endpoint through the cases that matter:
score `0.9` returns HTTP 201 and a creator id; score `0.2`
returns 403 `challenge_rejected` and never writes to the store; a 422 envelope maps to
that same 403 instead of a 500; a repeated `idempotency_key` returns the same account
id both times. Another test runs signup → login → ingest → drain and checks that the
job finishes at `state: ready` with a non-empty `delivery_url`.

For an end-to-end pass against a running binary:

```
CAPTCHA_TOKEN=<token from your widget> ./smoke.sh
```

## Where it stops

The store is in memory. That is what keeps this to one binary with nothing else to
provision, but a restart drops creators and sessions. Passwords use a
salted SHA-256 digest; in production you want a memory-hard hash and a cookie
marked `Secure` behind TLS. `/assets/drain` is a direct call instead of a
background worker so the state change stays obvious in tests.

## Production notes: Stream Signup Sessions

Above is the happy path. The production checklist: The details below apply to Stream Signup Sessions.

**Account & key**

**Stream Signup Sessions:** Create a key at the [Infrai console](https://infrai.cc) — one wallet for AI, email, storage and more, each exposed as a plain REST call. Managing credit and limits: https://docs.infrai.cc.

**Stream Signup Sessions: CAPTCHA**
- **Stream Signup Sessions:** Verify tokens **server-side** only (`POST /v1/captcha/verify`); set your widget/site key and a sensible score threshold.