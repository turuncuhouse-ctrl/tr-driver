package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"necipdrive/internal/domain"
	"necipdrive/internal/httpx"
)

type contextKey string

const userContextKey contextKey = "user"
const deviceContextKey contextKey = "device"

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"displayName"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	user, token, err := h.service.Register(r.Context(), req.Email, req.Password, req.DisplayName)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	http.SetCookie(w, h.service.SessionCookie(token, isHTTPS(r)))
	httpx.WriteJSON(w, http.StatusCreated, user)
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	result, err := h.service.Login(r.Context(), r.RemoteAddr, req.Email, req.Password)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrRateLimited) {
			status = http.StatusTooManyRequests
		} else if errors.Is(err, ErrMailRequired) {
			status = http.StatusBadRequest
		}
		httpx.Error(w, status, err.Error())
		return
	}
	if result.Requires2FA {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"requires2FA":    true,
			"challengeToken": result.ChallengeToken,
			"message":        "Doğrulama kodu e-posta adresinize gönderildi.",
		})
		return
	}
	http.SetCookie(w, h.service.SessionCookie(result.SessionToken, isHTTPS(r)))
	httpx.WriteJSON(w, http.StatusOK, result.User)
}

func (h *Handler) Login2FA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ChallengeToken string `json:"challengeToken"`
		Code           string `json:"code"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	user, token, err := h.service.CompleteLogin2FA(r.Context(), r.RemoteAddr, req.ChallengeToken, req.Code)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrRateLimited) {
			status = http.StatusTooManyRequests
		}
		httpx.Error(w, status, err.Error())
		return
	}
	http.SetCookie(w, h.service.SessionCookie(token, isHTTPS(r)))
	httpx.WriteJSON(w, http.StatusOK, user)
}

func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	err := h.service.ForgotPassword(r.Context(), r.RemoteAddr, req.Email)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrRateLimited) {
			status = http.StatusTooManyRequests
		}
		httpx.Error(w, status, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "Hesap varsa şifre sıfırlama kodu e-posta adresinize gönderildi.",
	})
}

func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Email       string `json:"email"`
		Code        string `json:"code"`
		NewPassword string `json:"newPassword"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	err := h.service.ResetPassword(r.Context(), r.RemoteAddr, req.Email, req.Code, req.NewPassword)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrRateLimited) {
			status = http.StatusTooManyRequests
		}
		httpx.Error(w, status, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "Şifreniz güncellendi. Yeni şifrenizle giriş yapabilirsiniz.",
	})
}

func (h *Handler) Security(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		st, err := h.service.SecurityStatus(r.Context(), user.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, st)
	case http.MethodPost:
		var req struct {
			Email2FAEnabled bool   `json:"email2FAEnabled"`
			Password        string `json:"password"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		if err := h.service.SetEmail2FA(r.Context(), user.ID, req.Password, req.Email2FAEnabled); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, ErrInvalidCredentials) {
				status = http.StatusUnauthorized
			}
			httpx.Error(w, status, err.Error())
			return
		}
		st, _ := h.service.SecurityStatus(r.Context(), user.ID)
		httpx.WriteJSON(w, http.StatusOK, st)
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if cookie, err := r.Cookie("session_token"); err == nil {
		_ = h.service.Logout(r.Context(), cookie.Value)
	}
	http.SetCookie(w, h.service.ClearCookie(isHTTPS(r)))
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (h *Handler) DeviceLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		DeviceName     string `json:"deviceName"`
		ChallengeToken string `json:"challengeToken"`
		Code           string `json:"code"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}

	var user *domain.User
	if req.ChallengeToken != "" && req.Code != "" {
		var sessionToken string
		var err error
		user, sessionToken, err = h.service.CompleteLogin2FA(r.Context(), r.RemoteAddr, req.ChallengeToken, req.Code)
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, ErrRateLimited) {
				status = http.StatusTooManyRequests
			}
			httpx.Error(w, status, err.Error())
			return
		}
		_ = h.service.Logout(r.Context(), sessionToken)
	} else {
		result, err := h.service.BeginDeviceLogin(r.Context(), r.RemoteAddr, req.Email, req.Password)
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, ErrRateLimited) {
				status = http.StatusTooManyRequests
			} else if errors.Is(err, ErrMailRequired) {
				status = http.StatusBadRequest
			}
			httpx.Error(w, status, err.Error())
			return
		}
		if result.Requires2FA {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{
				"requires2FA":    true,
				"challengeToken": result.ChallengeToken,
				"message":        "Doğrulama kodu e-posta adresinize gönderildi.",
			})
			return
		}
		user = result.User
	}

	device, token, err := h.service.CreateDevice(r.Context(), user.ID, req.DeviceName)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": user, "device": device, "token": token})
}

func (h *Handler) Devices(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	switch r.Method {
	case http.MethodGet:
		devices, err := h.service.ListDevices(r.Context(), user.ID)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, devices)
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := httpx.ReadJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid request")
			return
		}
		device, token, err := h.service.CreateDevice(r.Context(), user.ID, req.Name)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{"device": device, "token": token})
	default:
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) Device(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	deviceID := strings.TrimPrefix(r.URL.Path, "/api/auth/devices/")
	if deviceID == "" || strings.Contains(deviceID, "/") {
		httpx.Error(w, http.StatusBadRequest, "device id required")
		return
	}
	if err := h.service.RevokeDevice(r.Context(), UserFromContext(r.Context()).ID, deviceID); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// DeviceLogout revokes the current bearer device token (sync client logout).
func (h *Handler) DeviceLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	user := UserFromContext(r.Context())
	deviceID := DeviceIDFromContext(r.Context())
	if user == nil || deviceID == "" {
		httpx.Error(w, http.StatusUnauthorized, "device token required")
		return
	}
	if err := h.service.RevokeDevice(r.Context(), user.ID, deviceID); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, user)
}

func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if header := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(header), "bearer ") {
			token := strings.TrimSpace(header[len("Bearer "):])
			if token == "" {
				httpx.Error(w, http.StatusUnauthorized, "invalid device token")
				return
			}
			user, deviceID, err := h.service.UserByDeviceToken(r.Context(), token)
			if err != nil {
				httpx.Error(w, http.StatusUnauthorized, "invalid device token")
				return
			}
			ctx := WithDevice(context.WithValue(r.Context(), userContextKey, user), deviceID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		cookie, err := r.Cookie("session_token")
		if err != nil || cookie.Value == "" {
			httpx.Error(w, http.StatusUnauthorized, "login required")
			return
		}
		user, err := h.service.UserBySession(r.Context(), cookie.Value)
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, "invalid session")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey, user)))
	})
}

func (h *Handler) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session_token")
		if err != nil || cookie.Value == "" {
			httpx.Error(w, http.StatusUnauthorized, "login required")
			return
		}
		user, err := h.service.UserBySession(r.Context(), cookie.Value)
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, "invalid session")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey, user)))
	})
}

func UserFromContext(ctx context.Context) *domain.User {
	user, _ := ctx.Value(userContextKey).(*domain.User)
	return user
}

func WithDevice(ctx context.Context, deviceID string) context.Context {
	return context.WithValue(ctx, deviceContextKey, deviceID)
}

func DeviceIDFromContext(ctx context.Context) string {
	deviceID, _ := ctx.Value(deviceContextKey).(string)
	return deviceID
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}
