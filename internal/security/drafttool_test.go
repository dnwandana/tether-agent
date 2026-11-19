package security

import "testing"

func TestIsDraftTool(t *testing.T) {
	for name, want := range map[string]bool{
		"create_draft": true, "createDraft": true, "update-draft": true, "save_draft": true,
		"edit_draft": true, "new_draft": true, "drafts.add": true, "CREATE_DRAFT": true,
		"createHTMLDraft": true, "saveDraftEmail": true,
		"send_draft": false, "create_and_send_draft": false, "submit_draft": false,
		"schedule_draft": false, "dispatch_draft": false, "publish_draft": false,
		"create_draft_post": false, "list_drafts": false, "draft": false,
		"create_message": false, "createDraftedNote": false, "redraft_create": false, "": false,
	} {
		if got := IsDraftTool(name); got != want {
			t.Errorf("%q: got %v, want %v", name, got, want)
		}
	}
}
