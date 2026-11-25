#!/bin/sh
# Exercise the same FTS5 + content-table trigger pattern used by the application.
set -eu
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
sqlite3 "$work/fts.db" <<'SQL'
CREATE TABLE messages(id INTEGER PRIMARY KEY, subject TEXT, body_text TEXT);
CREATE VIRTUAL TABLE messages_fts USING fts5(subject,body_text,content='messages',content_rowid='id',tokenize='trigram');
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid,subject,body_text) VALUES(new.id,new.subject,new.body_text);
END;
CREATE TRIGGER messages_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts,rowid,subject,body_text) VALUES('delete',old.id,old.subject,old.body_text);
  INSERT INTO messages_fts(rowid,subject,body_text) VALUES(new.id,new.subject,new.body_text);
END;
INSERT INTO messages VALUES(1,'audit subject','original body');
UPDATE messages SET body_text='searchable replacement' WHERE id=1;
SQL
found=$(sqlite3 "$work/fts.db" "SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'replacement';")
[ "$found" = 1 ]
sqlite3 "$work/fts.db" ".backup '$work/snapshot.db'"
[ "$(sqlite3 "$work/snapshot.db" 'PRAGMA quick_check;')" = ok ]
[ "$(sqlite3 "$work/snapshot.db" "SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'replacement';")" = 1 ]
printf '%s\n' 'SQLite FTS5 triggers, query and snapshot: OK'
