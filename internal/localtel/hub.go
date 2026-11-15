package localtel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	maxSignalPayloadBytes = 32 << 10
	maxSignalCallIDLen    = 80
	maxSignalNumberLen    = 128
	maxSignalNameLen      = 120
	maxHubClientsPerUser  = 16
	clientSendBuffer      = 64
)

var ErrInvalidSignal = errors.New("invalid call signal")

type SignalMessage struct {
	Type        string `json:"type"`
	CallID      string `json:"callId"`
	Number      string `json:"number,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	// From is the caller's stable TR Driver address (extension/username). It is
	// filled by the server when a call crosses from one user to another.
	From        string `json:"from,omitempty"`
	Payload     string `json:"payload,omitempty"`
	DeviceID    string `json:"deviceId,omitempty"`
	TimestampMs int64  `json:"timestampMs,omitempty"`
}

func ValidateSignal(msg *SignalMessage) error {
	if msg == nil {
		return ErrInvalidSignal
	}
	msg.Type = strings.ToLower(strings.TrimSpace(msg.Type))
	switch msg.Type {
	case "invite", "ringing", "accept", "reject", "hangup", "cancel", "audio", "ping", "pong":
	default:
		return fmt.Errorf("%w: unknown signal type", ErrInvalidSignal)
	}
	if msg.Type == "ping" || msg.Type == "pong" {
		msg.CallID = strings.TrimSpace(msg.CallID)
		return nil
	}
	callID := strings.TrimSpace(msg.CallID)
	if len(callID) == 0 || len(callID) > maxSignalCallIDLen {
		return fmt.Errorf("%w: invalid callId", ErrInvalidSignal)
	}
	for _, r := range callID {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		if !ok {
			return fmt.Errorf("%w: unsafe callId", ErrInvalidSignal)
		}
	}
	msg.CallID = callID
	if hasControlOrNewline(msg.Number) || len(msg.Number) > maxSignalNumberLen {
		return fmt.Errorf("%w: invalid number", ErrInvalidSignal)
	}
	if hasControlOrNewline(msg.DisplayName) || len(msg.DisplayName) > maxSignalNameLen {
		return fmt.Errorf("%w: invalid displayName", ErrInvalidSignal)
	}
	if hasControlOrNewline(msg.From) || len(msg.From) > maxSignalNumberLen {
		return fmt.Errorf("%w: invalid from", ErrInvalidSignal)
	}
	if len(msg.Payload) > maxSignalPayloadBytes {
		return fmt.Errorf("%w: payload too large", ErrInvalidSignal)
	}
	if msg.TimestampMs == 0 {
		msg.TimestampMs = time.Now().UTC().UnixMilli()
	}
	return nil
}

type Client struct {
	id        string
	userID    string
	deviceID  string
	send      chan []byte
	closeOnce sync.Once
}

func (c *Client) SendChannel() <-chan []byte {
	return c.send
}

func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.send)
	})
}

// Hub fans out gateway-mode call signaling within one authenticated user's
// connected devices. No cross-user delivery is permitted.
type callRoute struct {
	callerUserID string
	calleeUserID string
	expiresAt    time.Time
}

type Hub struct {
	mu    sync.RWMutex
	users map[string]map[string]*Client
	calls map[string]callRoute
}

func NewHub() *Hub {
	return &Hub{
		users: make(map[string]map[string]*Client),
		calls: make(map[string]callRoute),
	}
}

func (h *Hub) Register(userID, deviceID string) *Client {
	client := &Client{
		id:       uuid.NewString(),
		userID:   userID,
		deviceID: deviceID,
		send:     make(chan []byte, clientSendBuffer),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	byUser, ok := h.users[userID]
	if !ok {
		byUser = make(map[string]*Client)
		h.users[userID] = byUser
	}
	if len(byUser) >= maxHubClientsPerUser {
		for oldID, old := range byUser {
			delete(byUser, oldID)
			old.close()
			break
		}
	}
	byUser[client.id] = client
	return client
}

func (h *Hub) Unregister(client *Client) {
	if client == nil {
		return
	}
	h.mu.Lock()
	if byUser, ok := h.users[client.userID]; ok {
		if current, exists := byUser[client.id]; exists && current == client {
			delete(byUser, client.id)
			if len(byUser) == 0 {
				delete(h.users, client.userID)
			}
			client.close()
		}
	}
	h.mu.Unlock()
}

func (h *Hub) ConnectedCount(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.users[userID])
}

// Dispatch delivers msg to all connected clients of userID except senderClientID.
func (h *Hub) Dispatch(userID, senderClientID string, msg SignalMessage) int {
	raw, err := json.Marshal(msg)
	if err != nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	byUser := h.users[userID]
	delivered := 0
	for id, c := range byUser {
		if senderClientID != "" && id == senderClientID {
			continue
		}
		select {
		case c.send <- raw:
			delivered++
		default:
			// Drop backlogged frames rather than stalling callers.
		}
	}
	return delivered
}
