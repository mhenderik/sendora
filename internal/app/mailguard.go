package app

import (
	"log"
	"strconv"
	"strings"
	"time"
)

// Outgoing mail abuse limits. A signed-in user could otherwise use the
// organisation's SMTP server to spam arbitrary addresses with attacker-chosen text.
const mailRecipientsPerHour = 100

// downloadMailMinInterval is a variable only so tests can lift the throttle.
var downloadMailMinInterval = 60 * time.Second

type mailJob struct {
	to, subject, key string
	data             map[string]any
}

func countRecipients(list []string) int {
	n := 0
	for _, r := range list {
		if strings.TrimSpace(r) != "" {
			n++
		}
	}
	return n
}

// takeMail charges n recipient emails against the user's hourly allowance.
func (s *Server) takeMail(userID int64, n int) bool {
	return s.allowN("mail:"+strconv.FormatInt(userID, 10), n, mailRecipientsPerHour, time.Hour)
}

// queueMail hands a message to a small fixed worker pool. When the queue is full
// the message is dropped with a log line instead of growing memory without bound
// (one goroutine per message used to be spawned).
func (s *Server) queueMail(to, subject, key string, data map[string]any) {
	s.mailOnce.Do(func() {
		for i := 0; i < 4; i++ {
			go func() {
				for job := range s.mailQueue {
					s.sendTemplate(job.to, job.subject, job.key, job.data)
				}
			}()
		}
	})
	select {
	case s.mailQueue <- mailJob{to, subject, key, data}:
	default:
		log.Printf("mail queue full: dropped a %s message", key)
	}
}

// downloadMailAllowed limits download notifications to one per share per minute
// so anonymous downloads cannot flood the owner's inbox or the outbox.
func (s *Server) downloadMailAllowed(shareID int64) bool {
	s.dlMailMu.Lock()
	defer s.dlMailMu.Unlock()
	now := time.Now()
	if s.dlMailLast == nil || len(s.dlMailLast) > 10000 {
		s.dlMailLast = make(map[int64]time.Time)
	}
	if last, ok := s.dlMailLast[shareID]; ok && now.Sub(last) < downloadMailMinInterval {
		return false
	}
	s.dlMailLast[shareID] = now
	return true
}
