package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const replyToHeaderLimit = 64 << 10

// backfillMessageReplyTo repairs historical rows without reading message bodies.
// Missing/unreadable files are retried on the next startup. Successful reads
// without Reply-To are remembered against their raw_path; filled rows are skipped.
func (a *App) backfillMessageReplyTo(ctx context.Context) error {
	rootPath := strings.TrimSpace(a.config().MaildirRoot)
	if rootPath == "" {
		return nil
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		if a.log != nil {
			a.log.Warn("reply-to backfill deferred: maildir root unavailable", "error", err)
		}
		return nil
	}
	defer root.Close()

	type candidate struct{ id, path string }
	lastID := ""
	updated, skipped := int64(0), 0
	for {
		rows, err := a.db.QueryContext(ctx, `SELECT m.id,m.raw_path FROM messages m WHERE m.reply_to='' AND m.raw_path<>'' AND m.id>? AND NOT EXISTS (SELECT 1 FROM system_settings s WHERE s.key='replyToHeaderChecked:'||m.id AND s.value=m.raw_path) ORDER BY m.id LIMIT 100`, lastID)
		if err != nil {
			return err
		}
		items := make([]candidate, 0, 100)
		for rows.Next() {
			var item candidate
			if err := rows.Scan(&item.id, &item.path); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if len(items) == 0 {
			break
		}
		lastID = items[len(items)-1].id
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			header, err := a.replyToRawHeader(root, rootPath, item.path)
			if err != nil {
				skipped++
				continue
			}
			replyTo := strings.TrimSpace(header.Get("Reply-To"))
			if replyTo == "" {
				// Only a successfully parsed header may be marked checked. Tie the
				// marker to raw_path so replacing/moving the file permits a retry.
				if _, err := a.db.ExecContext(ctx, `INSERT INTO system_settings(key,value,updated_at) SELECT ?,?,? WHERE EXISTS (SELECT 1 FROM messages WHERE id=? AND raw_path=? AND reply_to='') ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, "replyToHeaderChecked:"+item.id, item.path, a.now().UTC().Format(time.RFC3339Nano), item.id, item.path); err != nil {
					return err
				}
				continue
			}
			res, err := a.db.ExecContext(ctx, `UPDATE messages SET reply_to=? WHERE id=? AND raw_path=? AND reply_to=''`, validUTF8(replyTo), item.id, item.path)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			updated += n
		}
	}
	if a.log != nil && (updated > 0 || skipped > 0) {
		a.log.Info("reply-to backfill", "updated", updated, "deferred", skipped)
	}
	return nil
}

func (a *App) replyToRawHeader(root *os.Root, rootPath, path string) (textproto.MIMEHeader, error) {
	ok, err := a.pathIsUnderMaildirRoot(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("maildir path outside root")
	}
	rootAbs, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return nil, err
	}
	// Root also prevents a symlink inside Maildir from escaping the allowed root.
	info, err := root.Stat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("maildir message is not a regular file")
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readReplyToHeader(file)
}

func readReplyToHeader(source io.Reader) (textproto.MIMEHeader, error) {
	reader := bufio.NewReader(io.LimitReader(source, replyToHeaderLimit+1))
	var header bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if header.Len()+len(line) > replyToHeaderLimit {
			return nil, errors.New("mail header exceeds reply-to backfill limit")
		}
		header.WriteString(line)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			return textproto.NewReader(bufio.NewReader(bytes.NewReader(header.Bytes()))).ReadMIMEHeader()
		}
	}
}
