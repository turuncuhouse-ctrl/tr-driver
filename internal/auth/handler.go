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
	user, token, err := h.service.Login(r.Context(), r.RemoteAddr, req.Email, req.Password)
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
		Email      string `json:"email"`
		Password   string `json:"password"`
		DeviceName string `json:"deviceName"`
	}
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request")
		return
	}
	user, _, err := h.service.Login(r.Context(), r.RemoteAddr, req.Email, req.Password)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrRateLimited) {
			status = http.StatusTooManyRequests
		}
		httpx.Error(w, status, err.Error())
		return
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
		var req struct{ Name string `json:"name"` }
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
