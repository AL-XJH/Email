package app

import "testing"

func TestSanitizeAttachmentFilenameHandlesWindowsTraversal(t *testing.T) {
	cases := map[string]string{
		`..\..\evil.php`:     "evil.php",
		`C:\temp\note.txt`:   "note.txt",
		`sub/dir/report.pdf`: "report.pdf",
		``:                   "attachment.bin",
		`..`:                 "attachment.bin",
	}
	for input, want := range cases {
		if got := sanitizeAttachmentFilename(input); got != want {
			t.Fatalf("sanitizeAttachmentFilename(%q)=%q, want %q", input, got, want)
		}
	}
}
