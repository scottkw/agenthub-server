package mail

import (
	"context"
	"log/slog"
)

type noop struct{ log *slog.Logger }

// NewNoop returns a Mailer that logs the outbound message at INFO and
// returns nil. Useful for self-hosters who don't want to configure SMTP.
func NewNoop(log *slog.Logger) Mailer { return &noop{log: log} }

func (n *noop) Send(_ context.Context, msg Message) error {
	n.log.Info("mail.noop.send",
		"to", msg.To,
		"subject", msg.Subject,
		"text_bytes", len(msg.Text),
		"html_bytes", len(msg.HTML),
	)
	return nil
}
