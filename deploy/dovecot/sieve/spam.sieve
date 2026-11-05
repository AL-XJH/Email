require ["fileinto", "mailbox"];

# Rspamd 判定为垃圾的邮件，在 LMTP 投递阶段直接落到 Spam 文件夹。
#
# milter_headers 写入的是 "X-Spam-Status: Yes, score=15.5 required=6.0 ..."，
# 老版本 Rspamd 写的是 "X-Spam: Yes"。两种形式都处理。
# 放在投递阶段做，而不是事后扫描邮箱，这样不会和 IMAP 客户端搬信打架。
if anyof (
  header :matches "X-Spam-Status" "Yes*",
  header :matches "X-Spam" "Yes*"
) {
  fileinto :create "Spam";
  stop;
}
