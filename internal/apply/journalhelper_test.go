package apply

import "github.com/samansohani78/SixSync/internal/journal"

func journalOpen(path string) (*journal.Journal, error) { return journal.Open(path) }
