package services

// NewQueueIINAPlayer gives the queue its own instance of the shared process adapter.
func NewQueueIINAPlayer() *queueIINAPlayer { return NewControlledIINAPlayer() }
