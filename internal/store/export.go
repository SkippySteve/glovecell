package store

// Classify maps a pgx or network error to ErrUnavailable, ErrNotFound or
// ErrConflict exactly as this store does, for other stores that share the
// database conventions (internal/voice/store).
func Classify(err error) error { return classify(err) }
