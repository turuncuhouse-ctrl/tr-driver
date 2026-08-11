package admin

import (
	"net/http"
	"strings"

	"necipdrive/internal/auth"
	"necipdrive/internal/httpx"
	"necipdrive/internal/mailer"
)

type MailHandler struct {
	mail *mailer.Service
}

func NewMailHandler(mail *mailer.Service) *MailHandler {
	return &MailHandler{mail: mail}
}

func (h *MailHandler) Settings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		st, err := h.mail.Get(r.Context())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, st)
	case http.MethodPost:
		var req mailer.Settings
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		if err := h.mail.Save(r.Context(), req); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *MailHandler) Test(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		To string `json:"to"`
	}
	_ = httpx.ReadJSON(r, &req)
	to := strings.TrimSpace(req.To)
	if to == "" {
		if u := auth.UserFromContext(r.Context()); u != nil {
			to = u.Email
		}
	}
	if err := h.mail.SendTest(r.Context(), to); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"status":  "sent",
		"message": "Test e-postası gönderildi: " + to,
	})
}

func (h *MailHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	st, err := h.mail.Get(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"enabled": st.Enabled && st.Host != "" && st.From != ""})
}
