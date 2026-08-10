// Package syncclient implements the bearer-token sync API contract.
package syncclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Client struct {
	BaseURL, Token string
	HTTP           *http.Client
	ChunkBytes     int64
}
type Manifest struct {
	RelativePath     string `json:"relativePath"`
	FileName         string `json:"fileName"`
	MimeType         string `json:"mimeType"`
	ExpectedSize     int64  `json:"expectedSize"`
	LastModifiedMs   int64  `json:"lastModifiedMs"`
	TargetEntryID    string `json:"targetEntryId,omitempty"`
	ExpectedVersion  int64  `json:"expectedVersion,omitempty"`
	ContentHash      string `json:"contentHash,omitempty"`
	ClientModifiedAt int64  `json:"clientModifiedAt,omitempty"`
}
type Batch struct {
	ID    string          `json:"id"`
	Files []UploadSession `json:"files"`
}
type UploadSession struct {
	ID            string `json:"id"`
	RelativePath  string `json:"relativePath"`
	ReceivedBytes int64  `json:"receivedBytes"`
}
type SnapshotResponse struct {
	Cursor  int64   `json:"cursor"`
	Entries []Entry `json:"entries"`
}
type ChangesResponse struct {
	Cursor     int64    `json:"cursor"`
	NextCursor int64    `json:"nextCursor"`
	Changes    []Change `json:"changes"`
}
type Entry struct {
	ID             string  `json:"id"`
	ParentID       *string `json:"parentId"`
	Name           string  `json:"name"`
	Kind           string  `json:"kind"`
	SizeBytes      int64   `json:"sizeBytes"`
	ContentVersion int64   `json:"contentVersion"`
	ContentHash    string  `json:"contentHash"`
	DeletedAt      *string `json:"deletedAt,omitempty"`
	// Compat aliases used by engine helpers.
	Size    int64 `json:"-"`
	Version int64 `json:"-"`
}
type Change struct {
	Cursor         int64   `json:"cursor"`
	EntryID        string  `json:"entryId"`
	Op             string  `json:"op"`
	Name           string  `json:"name"`
	ParentID       *string `json:"parentId"`
	Kind           string  `json:"kind"`
	SizeBytes      int64   `json:"sizeBytes"`
	ContentVersion int64   `json:"contentVersion"`
	ContentHash    string  `json:"contentHash"`
	DeviceID       *string `json:"deviceId,omitempty"`
	// Compat aliases for engine.
	Type  string `json:"-"`
	Entry Entry  `json:"-"`
}

func (e *Entry) normalize() {
	e.Size = e.SizeBytes
	e.Version = e.ContentVersion
}
func (c *Change) normalize() {
	c.Type = c.Op
	c.Entry = Entry{
		ID:             c.EntryID,
		ParentID:       c.ParentID,
		Name:           c.Name,
		Kind:           c.Kind,
		SizeBytes:      c.SizeBytes,
		ContentVersion: c.ContentVersion,
		ContentHash:    c.ContentHash,
		Size:           c.SizeBytes,
		Version:        c.ContentVersion,
		DeletedAt:      nil,
	}
	if c.Op == "trash" || c.Op == "purge" {
		deleted := "true"
		c.Entry.DeletedAt = &deleted
	}
}
func (e Entry) Deleted() bool { return e.DeletedAt != nil }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}
func (c *Client) chunkBytes() int64 {
	if c.ChunkBytes > 0 {
		return c.ChunkBytes
	}
	return 8 << 20
}
func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	r, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if e != nil {
		return nil, e
	}
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return r, nil
}
func (c *Client) json(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	r, e := c.request(ctx, method, path, body)
	if e != nil {
		return e
	}
	if in != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, e := c.httpClient().Do(r)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
func apiError(r *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	return fmt.Errorf("sync API %s: %s", r.Status, strings.TrimSpace(string(b)))
}
func (c *Client) DeviceLogin(ctx context.Context, email, password, deviceName string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	e := c.json(ctx, http.MethodPost, "/api/auth/device-login", map[string]string{"email": email, "password": password, "deviceName": deviceName}, &out)
	if e != nil {
		return "", e
	}
	c.Token = out.Token
	return c.Token, nil
}

func (c *Client) DeviceLogout(ctx context.Context) error {
	if c.Token == "" {
		return nil
	}
	return c.json(ctx, http.MethodPost, "/api/auth/device-logout", map[string]string{}, nil)
}
func (c *Client) Me(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	e := c.json(ctx, http.MethodGet, "/api/auth/me", nil, &out)
	return out, e
}
func (c *Client) Snapshot(ctx context.Context) (SnapshotResponse, error) {
	var out SnapshotResponse
	e := c.json(ctx, http.MethodGet, "/api/sync/snapshot", nil, &out)
	for i := range out.Entries {
		out.Entries[i].normalize()
	}
	return out, e
}
func (c *Client) Changes(ctx context.Context, cursor int64, limit int) (ChangesResponse, error) {
	var out ChangesResponse
	e := c.json(ctx, http.MethodGet, "/api/sync/changes?cursor="+strconv.FormatInt(cursor, 10)+"&limit="+strconv.Itoa(limit), nil, &out)
	if out.NextCursor > 0 {
		out.Cursor = out.NextCursor
	}
	for i := range out.Changes {
		out.Changes[i].normalize()
	}
	return out, e
}
func (c *Client) CreateFolder(ctx context.Context, parentID, name string) (Entry, error) {
	var out Entry
	e := c.json(ctx, http.MethodPost, "/api/sync/folders", map[string]string{"parentId": parentID, "name": name}, &out)
	out.normalize()
	return out, e
}
func (c *Client) Rename(ctx context.Context, id, name string) error {
	return c.json(ctx, http.MethodPost, "/api/sync/rename", map[string]string{"fileId": id, "name": name}, nil)
}
func (c *Client) Move(ctx context.Context, id, parentID string) error {
	return c.json(ctx, http.MethodPost, "/api/sync/move", map[string]string{"fileId": id, "parentId": parentID}, nil)
}
func (c *Client) Trash(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodPost, "/api/sync/trash", map[string]string{"fileId": id}, nil)
}
func (c *Client) Restore(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodPost, "/api/sync/restore", map[string]string{"fileId": id}, nil)
}
func (c *Client) CreateBatch(ctx context.Context, parentID string, files []Manifest) (Batch, error) {
	var out Batch
	e := c.json(ctx, http.MethodPost, "/api/uploads/batches", map[string]any{"parentId": parentID, "files": files}, &out)
	return out, e
}
func (c *Client) PutChunk(ctx context.Context, sessionID string, offset int64, body io.Reader, size int64) (int64, error) {
	r, e := c.request(ctx, http.MethodPut, "/api/uploads/files/"+sessionID, body)
	if e != nil {
		return 0, e
	}
	r.Header.Set("Content-Type", "application/octet-stream")
	r.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	r.ContentLength = size
	resp, e := c.httpClient().Do(r)
	if e != nil {
		return 0, e
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		n, _ := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
		return n, fmt.Errorf("upload offset conflict: server=%d", n)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, apiError(resp)
	}
	if n, e := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64); e == nil {
		return n, nil
	}
	var out struct {
		Offset int64 `json:"offset"`
	}
	if e = json.NewDecoder(resp.Body).Decode(&out); e != nil {
		return 0, e
	}
	return out.Offset, nil
}
func (c *Client) CompleteFile(ctx context.Context, sessionID string) (Entry, error) {
	var out Entry
	e := c.json(ctx, http.MethodPost, "/api/uploads/files/"+sessionID+"/complete", map[string]any{}, &out)
	out.normalize()
	return out, e
}
func (c *Client) Download(ctx context.Context, fileID, dest string, resume bool) error {
	partial := dest + ".partial"
	var offset int64
	if resume {
		if info, e := os.Stat(partial); e == nil {
			offset = info.Size()
		}
	}
	r, e := c.request(ctx, http.MethodGet, "/api/files/download/"+fileID, nil)
	if e != nil {
		return e
	}
	if offset > 0 {
		r.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, e := c.httpClient().Do(r)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp)
	}
	if resp.StatusCode == http.StatusOK {
		offset = 0
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
		return e
	}
	f, e := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|map[bool]int{true: os.O_APPEND, false: os.O_TRUNC}[offset > 0], 0644)
	if e != nil {
		return e
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(partial, dest)
}
func (c *Client) UploadFile(ctx context.Context, parentID, path string, m Manifest) (Entry, error) {
	info, e := os.Stat(path)
	if e != nil {
		return Entry{}, e
	}
	if m.FileName == "" {
		m.FileName = filepath.Base(path)
	}
	if m.RelativePath == "" {
		m.RelativePath = m.FileName
	}
	if m.ExpectedSize == 0 {
		m.ExpectedSize = info.Size()
	}
	if m.LastModifiedMs == 0 {
		m.LastModifiedMs = info.ModTime().UnixMilli()
	}
	if m.MimeType == "" {
		m.MimeType = mime.TypeByExtension(filepath.Ext(path))
		if m.MimeType == "" {
			m.MimeType = "application/octet-stream"
		}
	}
	b, e := c.CreateBatch(ctx, parentID, []Manifest{m})
	if e != nil {
		return Entry{}, e
	}
	if len(b.Files) == 0 {
		return Entry{}, fmt.Errorf("batch returned no upload session")
	}
	session := b.Files[0]
	f, e := os.Open(path)
	if e != nil {
		return Entry{}, e
	}
	defer f.Close()
	offset := session.ReceivedBytes
	buf := make([]byte, c.chunkBytes())
	for offset < info.Size() {
		n, readErr := f.ReadAt(buf, offset)
		if n > 0 {
			next, e := c.PutChunk(ctx, session.ID, offset, bytes.NewReader(buf[:n]), int64(n))
			if e != nil {
				return Entry{}, e
			}
			offset = next
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return Entry{}, readErr
		}
	}
	return c.CompleteFile(ctx, session.ID)
}
