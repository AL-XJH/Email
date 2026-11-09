#!/bin/sh
set -eu

# 只支持 SQLite：Postfix 通过 sqlite map 直接读 /data/lanqin.db。
postconf -e 'virtual_mailbox_domains = sqlite:/etc/postfix/sqlite-domains.cf'
postconf -e 'virtual_mailbox_maps = sqlite:/etc/postfix/sqlite-mailboxes.cf'
postconf -e 'virtual_alias_maps = sqlite:/etc/postfix/sqlite-aliases.cf'
postconf -e 'recipient_bcc_maps = sqlite:/etc/postfix/sqlite-collection-bcc.cf'

# 地址别名映射仅用于地址重写，不能用于判断域名类别。
postconf -e 'virtual_alias_domains ='
