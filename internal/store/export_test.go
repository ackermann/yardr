package store

// MentionCompiles is how many @mention patterns the store has compiled.
func (s *Store) MentionCompiles() int64 { return s.mentions.compiled.Load() }
