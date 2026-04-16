// Package mail defines the Mailer interface and ships two implementations:
// noop (dev/solo without SMTP) and smtp.
package mail

import "context"

// Message is a single outbound email. HTML body is optional.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer sends transactional email. Implementations:
//   - NewNoop: logs and discards (default for solo mode with mail.provider=noop)
//   - NewSMTP: classic SMTP with AUTH
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}
