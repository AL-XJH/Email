package app

import (
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestExternalIMAPInlineBodyUIDsSkipsLargeMessages(t *testing.T) {
	meta := []*imapclient.FetchMessageBuffer{
		{UID: 1, RFC822Size: 1024},
		{UID: 2, RFC822Size: externalIMAPInlineBodyMaxBytes},
		{UID: 3, RFC822Size: externalIMAPInlineBodyMaxBytes + 1},
		{UID: 4, RFC822Size: 30 << 20},
		nil,
		{UID: 5},
	}
	set := externalIMAPInlineBodyUIDs(meta, externalIMAPInlineBodyMaxBytes)
	got := map[imap.UID]bool{}
	for _, item := range set {
		for uid := item.Start; uid <= item.Stop; uid++ {
			got[uid] = true
		}
	}
	for _, uid := range []imap.UID{1, 2, 5} {
		if !got[uid] {
			t.Fatalf("uid %d should be fetched inline", uid)
		}
	}
	for _, uid := range []imap.UID{3, 4} {
		if got[uid] {
			t.Fatalf("uid %d is over the limit and must be fetched one by one", uid)
		}
	}
	if len(got) != 3 {
		t.Fatalf("inline uid count = %d, want 3 (%v)", len(got), got)
	}
}

func TestExternalIMAPInlineBodyUIDsZeroLimitKeepsAll(t *testing.T) {
	meta := []*imapclient.FetchMessageBuffer{
		{UID: 7, RFC822Size: 100 << 20},
	}
	if count := staticUIDSetCount(externalIMAPInlineBodyUIDs(meta, 0)); count != 1 {
		t.Fatalf("count = %d, want 1 when limit is disabled", count)
	}
}

func TestExternalIMAPMaxMessageBytes(t *testing.T) {
	a := newTestApp(t)
	a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPMaxMessageMB = 50 })
	if got, want := a.externalIMAPMaxMessageBytes(), int64(50)<<20; got != want {
		t.Fatalf("limit = %d, want %d", got, want)
	}
	a.updateConfig(func(cfg *Config) { cfg.ExternalIMAPMaxMessageMB = 0 })
	if got := a.externalIMAPMaxMessageBytes(); got != 0 {
		t.Fatalf("unlimited limit = %d, want 0", got)
	}
}
