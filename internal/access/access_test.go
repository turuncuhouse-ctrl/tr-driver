package access

import "testing"

func TestRoleLevels(t *testing.T) {
	if requiredLevel(ActionView) >= requiredLevel(ActionManage) {
		t.Fatal("view should be weaker than manage")
	}
	if requiredLevel(ActionManage) >= requiredLevel(ActionAdminDrive) {
		t.Fatal("manage should be weaker than admin_drive")
	}
	if roleLevel["viewer"] >= roleLevel["manager"] {
		t.Fatal("viewer should be below manager")
	}
	if roleLevel["editor"] != roleLevel["contributor"] {
		t.Fatal("editor should map to contributor level")
	}
	if roleLevel["content_manager"] < requiredLevel(ActionManage) {
		t.Fatal("content_manager should manage content")
	}
	if roleLevel["content_manager"] >= requiredLevel(ActionAdminDrive) {
		t.Fatal("content_manager must not admin drives")
	}
}
