package ids

import "github.com/google/uuid"

// NewUID генерирует UUID v4 в строковом формате.
func NewUID() string { return uuid.NewString() }
