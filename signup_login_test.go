package streamauth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stubCaptcha stands in for POST /v1/captcha/verify so the decision table runs
// without leaving the machine.
func stubCaptcha(t *testing.T, status int, payload string) *CaptchaGate {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("captcha called with %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("missing bearer key, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return &CaptchaGate{BaseURL: srv.URL, APIKey: "test-key", HTTP: srv.Client()}
}

func TestSignUpDecision(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		payload      string
		email        string
		password     string
		wantStatus   int
		wantErrCode  string
		repeatSignUp bool
	}{
		{
			name:       "human passes and the account is created",
			status:     200,
			payload:    `{"ok":true,"data":{"success":true,"score":0.9,"action":"creator_signup"}}`,
			email:      "Nina@Studio.example",
			password:   "long-enough-passphrase",
			wantStatus: http.StatusCreated,
		},
		{
			name:        "score under the floor never reaches the store",
			status:      200,
			payload:     `{"ok":true,"data":{"success":true,"score":0.2,"action":"creator_signup"}}`,
			email:       "drive-by@studio.example",
			password:    "long-enough-passphrase",
			wantStatus:  http.StatusForbidden,
			wantErrCode: "challenge_rejected",
		},
		{
			name:        "envelope decision on a 4xx stays a 4xx for our caller",
			status:      422,
			payload:     `{"ok":false,"error":{"code":"CAPTCHA_SCORE_TOO_LOW","message":"below threshold"}}`,
			email:       "bot@studio.example",
			password:    "long-enough-passphrase",
			wantStatus:  http.StatusForbidden,
			wantErrCode: "challenge_rejected",
		},
		{
			name:        "short passphrase is refused after the human check",
			status:      200,
			payload:     `{"ok":true,"data":{"success":true,"score":0.9}}`,
			email:       "short@studio.example",
			password:    "abc",
			wantStatus:  http.StatusBadRequest,
			wantErrCode: "password_too_short",
		},
		{
			name:         "second signup on a taken email conflicts",
			status:       200,
			payload:      `{"ok":true,"data":{"success":true,"score":0.9}}`,
			email:        "twice@studio.example",
			password:     "long-enough-passphrase",
			repeatSignUp: true,
			wantStatus:   http.StatusConflict,
			wantErrCode:  "email_taken",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(stubCaptcha(t, tc.status, tc.payload), "https://cdn.example/stream")
			if tc.repeatSignUp {
				post(t, svc, "/signup", signUpBody{Email: tc.email, Password: tc.password, CaptchaToken: "tok"})
			}
			rec := post(t, svc, "/signup", signUpBody{Email: tc.email, Password: tc.password, CaptchaToken: "tok"})
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantErrCode != "" {
				var out struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				json.Unmarshal(rec.Body.Bytes(), &out)
				if out.Error.Code != tc.wantErrCode {
					t.Fatalf("error code = %q, want %q", out.Error.Code, tc.wantErrCode)
				}
			}
		})
	}
}

func TestRetriedSignUpKeepsOneAccount(t *testing.T) {
	svc := NewService(stubCaptcha(t, 200, `{"ok":true,"data":{"success":true,"score":0.9}}`), "https://cdn.example/stream")
	body := signUpBody{Email: "retry@studio.example", Password: "long-enough-passphrase", CaptchaToken: "tok", IdempotencyKey: "sig-77"}

	first := post(t, svc, "/signup", body)
	second := post(t, svc, "/signup", body)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("statuses = %d,%d want 201,201", first.Code, second.Code)
	}
	if idOf(t, first) != idOf(t, second) {
		t.Fatalf("retry made a second account: %s vs %s", idOf(t, first), idOf(t, second))
	}
}

func TestSessionCarriesIngestThroughToDelivery(t *testing.T) {
	svc := NewService(stubCaptcha(t, 200, `{"ok":true,"data":{"success":true,"score":0.9}}`), "https://cdn.example/stream")
	post(t, svc, "/signup", signUpBody{Email: "nina@studio.example", Password: "long-enough-passphrase", CaptchaToken: "tok"})

	login := post(t, svc, "/login", logInBody{Email: "nina@studio.example", Password: "long-enough-passphrase"})
	var sess struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(login.Body.Bytes(), &sess)
	if sess.SessionID == "" {
		t.Fatalf("no session issued: %s", login.Body.String())
	}

	if rec := postAs(t, svc, "/assets", "", ingestBody{Title: "ep-1", SourceKey: "masters/ep-1.mov"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous ingest = %d, want 401", rec.Code)
	}

	rec := postAs(t, svc, "/assets", sess.SessionID, ingestBody{Title: "ep-1", SourceKey: "masters/ep-1.mov", UploadID: "up-1"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("ingest = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	postAs(t, svc, "/assets/drain", sess.SessionID, map[string]string{})

	list := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/assets", nil)
	req.AddCookie(&http.Cookie{Name: "sid", Value: sess.SessionID})
	svc.Routes().ServeHTTP(list, req)

	var out struct {
		Jobs []IngestJob `json:"jobs"`
	}
	json.Unmarshal(list.Body.Bytes(), &out)
	if len(out.Jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(out.Jobs))
	}
	if out.Jobs[0].State != "ready" || out.Jobs[0].DeliveryURL == "" {
		t.Fatalf("job did not reach delivery: %+v", out.Jobs[0])
	}
}

func TestExpiredSessionStopsResolving(t *testing.T) {
	store := NewStore()
	store.TTL = time.Minute
	if _, err := store.SignUp("gone@studio.example", "long-enough-passphrase", ""); err != nil {
		t.Fatal(err)
	}
	sess, err := store.LogIn("gone@studio.example", "long-enough-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	base := store.Now()
	store.Now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := store.Resolve(sess.ID); err != ErrSessionNotFound {
		t.Fatalf("expired session resolved: %v", err)
	}
}

func post(t *testing.T, svc *Service, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return postAs(t, svc, path, "", body)
}

func postAs(t *testing.T, svc *Service, path, sessionID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	if sessionID != "" {
		req.AddCookie(&http.Cookie{Name: "sid", Value: sessionID})
	}
	rec := httptest.NewRecorder()
	svc.Routes().ServeHTTP(rec, req)
	return rec
}

func idOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		CreatorID string `json:"creator_id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out.CreatorID
}
