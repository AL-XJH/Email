package app

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestExternalIMAPFolderRoleForGmailChineseNames(t *testing.T) {
	cases := map[string]string{
		"[Gmail]/垃圾邮件":  "Spam",
		"[Gmail]/已删除邮件": "Trash",
		"[Gmail]/已发邮件":  "Sent",
		"[Gmail]/草稿":    "Drafts",
		"[Gmail]/存档":    "Archive",
		"垃圾邮件":         "Spam",
		"草稿箱":          "Drafts",
	}
	for name, want := range cases {
		if got := normalizeExternalIMAPFolderName(externalIMAPStripFolderPrefix(name)); got != want {
			t.Fatalf("role(%q)=%q want %q", name, got, want)
		}
	}
}

func TestExternalIMAPFolderRoleFromAttrs(t *testing.T) {
	cases := []struct {
		attrs []imap.MailboxAttr
		want  string
	}{
		{[]imap.MailboxAttr{imap.MailboxAttrJunk}, "Spam"},
		{[]imap.MailboxAttr{imap.MailboxAttrTrash}, "Trash"},
		{[]imap.MailboxAttr{imap.MailboxAttrSent}, "Sent"},
		{[]imap.MailboxAttr{imap.MailboxAttrDrafts}, "Drafts"},
		{[]imap.MailboxAttr{imap.MailboxAttrArchive}, "Archive"},
		{[]imap.MailboxAttr{imap.MailboxAttrHasNoChildren}, ""},
	}
	for _, tc := range cases {
		if got := externalIMAPFolderRoleFromAttrs(tc.attrs); got != tc.want {
			t.Fatalf("attrs %v -> %q want %q", tc.attrs, got, tc.want)
		}
	}
}
