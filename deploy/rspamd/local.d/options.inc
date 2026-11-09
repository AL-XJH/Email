# Rspamd 的 DNS 解析器。
#
# 默认会走容器里的系统解析器，也就是上游的公共 DNS。Spamhaus（zen/dbl）和
# URIBL 都会拒绝来自公共解析器的查询，返回 127.255.255.254 / 127.0.0.1 这类
# "查询被拒"的错误码。Rspamd 看到这种返回值会把对应黑名单标记为不可靠并停用，
# 结果就是 DNSBL 全部失效，日志里只剩一句 "likely DNS spoofing or BL issues"。
#
# 这里必须指向**本地递归解析器**（unbound / knot-resolver / 自建 BIND 递归），
# 不能是转发器：转发器把查询甩给上游，Spamhaus 看到的仍然是上游的 IP。
#
# __LANQIN_RSPAMD_DNS__ 由 entrypoint 在启动时用 LANQIN_RSPAMD_DNS 替换。
dns {
  nameserver = ["__LANQIN_RSPAMD_DNS__"];
  timeout = 2s;
  retransmits = 2;
}

# 单任务总超时必须大于最慢符号的超时，否则 rspamd 启动自检会报
# "configured task_timeout 8.00 is less than maximum symbols cache timeout 15.00"，
# 并且 ARC_CHECK / ARC_SIGNED（各 10s）会被 8s 的 task_timeout 提前掐断，
# 结果是 ARC 结果不稳定、时有时无。20s 只是个上限，正常邮件仍然是毫秒级返回。
task_timeout = 20s;
