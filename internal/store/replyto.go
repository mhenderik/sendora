package store

// migrateReplyToFormat removes the angle brackets an earlier version stored around
// a bare Reply-To address ("<a@b.c>" becomes "a@b.c"). Values with a name are kept.
func (s *Store) migrateReplyToFormat() error {
	_, err := s.DB.Exec(`UPDATE settings SET value=substr(value,2,length(value)-2)
		WHERE key='smtp_reply_to' AND value LIKE '<%@%>' AND instr(value,' ')=0`)
	return err
}
