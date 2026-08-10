package admin

import (
	"context"
	"testing"
)

func TestSetQuotaRejectsInvalidValues(t *testing.T) {
	service := &Service{}
	if err := service.SetQuota(context.Background(), "user", -1); err == nil {
		t.Fatal("expected negative quota to fail")
	}
}

func TestSetRoleRejectsUnknownRole(t *testing.T) {
	service := &Service{}
	if err := service.SetRole(context.Background(), "actor", "user", "owner"); err == nil {
		t.Fatal("expected unknown role to fail")
	}
}
