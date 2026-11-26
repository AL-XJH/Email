package app

import (
	"errors"
	"fmt"
)

// Keep the existing Postfix limit; this is also enforced by deploy entrypoints.
const postfixMessageSizeLimit int64 = 37748736

var errMessageTooLarge = errors.New("邮件编码后大小超过发送上限")

func outboundMessageSizeLimit(cfg Config) int64 {
	if cfg.SubmissionMaxMessageMB >= 36 {
		return postfixMessageSizeLimit
	}
	limit := int64(cfg.SubmissionMaxMessageMB) * 1024 * 1024
	if cfg.SubmissionMaxMessageMB <= 0 {
		limit = 35 << 20
	}
	if limit > postfixMessageSizeLimit || limit <= 0 {
		return postfixMessageSizeLimit
	}
	return limit
}

func validateOutboundMessageSize(cfg Config, size int64) error {
	limit := outboundMessageSizeLimit(cfg)
	if size > limit {
		return fmt.Errorf("%w（%d 字节，约 %.2f MiB），请减少附件或正文后重试", errMessageTooLarge, limit, float64(limit)/(1024*1024))
	}
	return nil
}
