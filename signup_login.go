package streamauth

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
)

// Service wires the human check, the session store and the ingest queue behind
// a handful of JSON routes.
type Service struct {
	Store   *Store
	Queue   *Queue
	Captcha *CaptchaGate
	// MinScore is the human-likelihood floor a signup must clear.
	MinScore float64
}

func NewService(gate *CaptchaGate, cdnBase string) *Service {
	return &Service{
		Store:    NewStore(),
		Queue:    NewQueue(cdnBase),
		Captcha:  gate,
		MinScore: 0.5,
	}
}

func (s *Service) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/signup", s.handleSignUp)
	mux.HandleFunc("/login", s.handleLogIn)
	mux.HandleFunc("/logout", s.handleLogOut)
	mux.HandleFunc("/assets", s.handleAssets)
	mux.HandleFunc("/assets/drain", s.handleDrain)
	return mux
}

type signUpBody struct {
	Email          string `json:"email"`
	Password       string `json:"password"`
	WidgetRecordID string `json:"widget_record_id"`
	CaptchaToken   string `json:"captcha_token"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Service) handleSignUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	var body signUpBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "request body is not JSON")
		return
	}

	result, err := s.Captcha.Verify(CaptchaRequest{
		WidgetRecordID: body.WidgetRecordID,
		Token:          body.CaptchaToken,
		Action:         "creator_signup",
		IP:             clientIP(r),
		ScoreThreshold: s.MinScore,
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			// A decision about this signup, so it becomes a 4xx for our caller.
			writeErr(w, http.StatusForbidden, "challenge_rejected", "the human check did not pass")
			return
		}
		writeErr(w, http.StatusBadGateway, "challenge_unreachable", "could not complete the human check")
		return
	}
	if !result.Success || result.Score < s.MinScore {
		writeErr(w, http.StatusForbidden, "challenge_rejected", "the human check did not pass")
		return
	}

	creator, err := s.Store.SignUp(body.Email, body.Password, body.IdempotencyKey)
	switch {
	case errors.Is(err, ErrEmailTaken):
		writeErr(w, http.StatusConflict, "email_taken", "that email already has an account")
		return
	case errors.Is(err, ErrPasswordTooShort):
		writeErr(w, http.StatusBadRequest, "password_too_short", err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "signup_failed", "could not create the account")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"creator_id": creator.ID,
		"email":      creator.Email,
		"score":      result.Score,
	})
}

type logInBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Service) handleLogIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	var body logInBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "request body is not JSON")
		return
	}
	sess, err := s.Store.LogIn(body.Email, body.Password)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "bad_credentials", "email or password does not match")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "sid",
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  sess.ExpiresAt,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sess.ID,
		"expires_at": sess.ExpiresAt,
	})
}

func (s *Service) handleLogOut(w http.ResponseWriter, r *http.Request) {
	if sid := sessionID(r); sid != "" {
		s.Store.LogOut(sid)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ended": true})
}

type ingestBody struct {
	Title     string `json:"title"`
	SourceKey string `json:"source_key"`
	UploadID  string `json:"upload_id"`
}

func (s *Service) handleAssets(w http.ResponseWriter, r *http.Request) {
	creator, err := s.Store.Resolve(sessionID(r))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "no_session", "log in first")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"jobs": s.Queue.List(creator.ID)})
	case http.MethodPost:
		var body ingestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_json", "request body is not JSON")
			return
		}
		if body.SourceKey == "" {
			writeErr(w, http.StatusBadRequest, "missing_source_key", "source_key is required")
			return
		}
		job := s.Queue.Enqueue(creator.ID, body.Title, body.SourceKey, body.UploadID)
		writeJSON(w, http.StatusAccepted, job)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

func (s *Service) handleDrain(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Store.Resolve(sessionID(r)); err != nil {
		writeErr(w, http.StatusUnauthorized, "no_session", "log in first")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"processed": s.Queue.Drain()})
}

func sessionID(r *http.Request) string {
	if c, err := r.Cookie("sid"); err == nil && c.Value != "" {
		return c.Value
	}
	return strings.TrimPrefix(r.Header.Get("X-Session-Id"), "Bearer ")
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
