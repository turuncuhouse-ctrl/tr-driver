package domain

import "time"

type User struct {
	ID               string    `json:"id"`
	Email            string    `json:"email"`
	PasswordHash     string    `json:"-"`
	DisplayName      string    `json:"displayName"`
	Role             string    `json:"role"`
	PlanCode         string    `json:"planCode"`
	QuotaBytes       int64     `json:"quotaBytes"` // effective = base + bonus
	BaseQuotaBytes   int64     `json:"baseQuotaBytes,omitempty"`
	BonusQuotaBytes  int64     `json:"bonusQuotaBytes,omitempty"`
	UsedBytes        int64     `json:"usedBytes"`
	ReservedBytes    int64     `json:"reservedBytes"`
	CreatedAt        time.Time `json:"createdAt"`
	LastLoginAt      time.Time `json:"lastLoginAt"`
	StorageRootID    string    `json:"storageRootId"`
	PersonalDriveID  string    `json:"personalDriveId,omitempty"`
	MaxBatchBytes    int64     `json:"maxBatchBytes,omitempty"`
	UploadChunkBytes int64     `json:"uploadChunkBytes,omitempty"`
	DeviceID         *string   `json:"-"`
}

type UploadBatch struct {
	ID            string          `json:"id"`
	ParentID      *string         `json:"parentId"`
	TotalBytes    int64           `json:"totalBytes"`
	ReservedBytes int64           `json:"reservedBytes"`
	FileCount     int             `json:"fileCount"`
	Status        string          `json:"status"`
	ExpiresAt     time.Time       `json:"expiresAt"`
	CreatedAt     time.Time       `json:"createdAt"`
	Files         []UploadSession `json:"files,omitempty"`
}

type UploadSession struct {
	ID               string     `json:"id"`
	BatchID          string     `json:"batchId"`
	ParentID         *string    `json:"parentId"`
	RelativePath     string     `json:"relativePath"`
	FileName         string     `json:"fileName"`
	MimeType         string     `json:"mimeType"`
	ExpectedSize     int64      `json:"expectedSize"`
	ReceivedBytes    int64      `json:"receivedBytes"`
	LastModifiedMs   int64      `json:"lastModifiedMs"`
	TargetEntryID    *string    `json:"targetEntryId,omitempty"`
	ExpectedVersion  *int64     `json:"expectedVersion,omitempty"`
	ContentHash      string     `json:"contentHash"`
	ClientModifiedAt *time.Time `json:"clientModifiedAt,omitempty"`
	DeviceID         *string    `json:"deviceId,omitempty"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

type Session struct {
	ID        string
	UserID    string
	TokenHash string
	ExpiresAt time.Time
	CreatedAt time.Time
}

type FileEntry struct {
	ID               string     `json:"id"`
	UserID           string     `json:"userId"`
	DriveID          string     `json:"driveId,omitempty"`
	ParentID         *string    `json:"parentId"`
	Name             string     `json:"name"`
	Kind             string     `json:"kind"`
	StorageKey       string     `json:"-"`
	SizeBytes        int64      `json:"sizeBytes"`
	MimeType         string     `json:"mimeType"`
	ContentVersion   int64      `json:"contentVersion"`
	ContentHash      string     `json:"contentHash"`
	ClientModifiedAt *time.Time `json:"clientModifiedAt,omitempty"`
	LastOpenedAt     *time.Time `json:"lastOpenedAt,omitempty"`
	DeletedAt        *time.Time `json:"deletedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	Starred          bool       `json:"starred,omitempty"`
}

type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

type FileChange struct {
	ID               int64      `json:"cursor"`
	EntryID          string     `json:"entryId"`
	Op               string     `json:"op"`
	Name             string     `json:"name"`
	ParentID         *string    `json:"parentId"`
	Kind             string     `json:"kind"`
	SizeBytes        int64      `json:"sizeBytes"`
	MimeType         string     `json:"mimeType"`
	ContentVersion   int64      `json:"contentVersion"`
	ContentHash      string     `json:"contentHash"`
	DeviceID         *string    `json:"deviceId,omitempty"`
	ClientModifiedAt *time.Time `json:"clientModifiedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
}

type ShareLink struct {
	ID              string     `json:"id"`
	EntryID         string     `json:"entryId"`
	FileID          string     `json:"fileId"` // compat alias of EntryID
	Token           string     `json:"token,omitempty"`
	PasswordHash    string     `json:"-"`
	HasPassword     bool       `json:"hasPassword,omitempty"`
	Permission      string     `json:"permission"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	DownloadCount   int64      `json:"downloadCount"`
	MaxDownloads    *int64     `json:"maxDownloads,omitempty"`
	CreatedByUserID string     `json:"createdByUserId"`
	CreatedAt       time.Time  `json:"createdAt"`
	EntryName       string     `json:"entryName,omitempty"`
	EntryKind       string     `json:"entryKind,omitempty"`
}

type Plan struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	QuotaBytes  int64  `json:"quotaBytes"`
	PriceCents  int64  `json:"priceCents"`
	BillingTerm string `json:"billingTerm"`
	Active      bool   `json:"active"`
}

type Drive struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"` // personal | shared
	Name        string    `json:"name"`
	OwnerUserID string    `json:"ownerUserId"`
	RootEntryID string    `json:"rootEntryId"`
	MyRole      string    `json:"myRole,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type DriveMember struct {
	UserID      string    `json:"userId"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"createdAt"`
}

type FilePermission struct {
	ID           string    `json:"id"`
	EntryID      string    `json:"entryId"`
	GranteeUserID string   `json:"granteeUserId"`
	Email        string    `json:"email,omitempty"`
	DisplayName  string    `json:"displayName,omitempty"`
	Role         string    `json:"role"` // viewer | commenter | editor
	CreatedAt    time.Time `json:"createdAt"`
}

type FileVersion struct {
	ID             string    `json:"id"`
	EntryID        string    `json:"entryId"`
	Version        int64     `json:"version"`
	SizeBytes      int64     `json:"sizeBytes"`
	MimeType       string    `json:"mimeType"`
	ContentHash    string    `json:"contentHash"`
	CreatedByUserID string   `json:"createdByUserId"`
	CreatedAt      time.Time `json:"createdAt"`
}

type FileComment struct {
	ID         string    `json:"id"`
	EntryID    string    `json:"entryId"`
	UserID     string    `json:"userId"`
	Email      string    `json:"email,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Activity struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId"`
	ActorID   string    `json:"actorId"`
	ActorName string    `json:"actorName,omitempty"`
	Kind      string    `json:"kind"`
	EntryID   string    `json:"entryId,omitempty"`
	DriveID   string    `json:"driveId,omitempty"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

type Notification struct {
	ID        string     `json:"id"`
	UserID    string     `json:"userId"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	EntryID   string     `json:"entryId,omitempty"`
	DriveID   string     `json:"driveId,omitempty"`
	ReadAt    *time.Time `json:"readAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}
